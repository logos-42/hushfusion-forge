#!/usr/bin/env bash
# 24h 持续运作包装器 —— 崩溃自动重启 + 日志轮转 + 状态文件。
# 用法(服务器): setsid nohup bash scripts/oml_supervisor.sh > /work/liuyuanjie/oml_supervisor.out 2>&1 &
# 停: touch /work/liuyuanjie/design_daemon_oml.stop  (优雅停, supervisor 一并退)
# 说明: supervisor 循环尝试拉起 design_daemon_oml.py; 若进程崩溃(crash)则自动重启,
#        checkpoint(ckpt) 保证累积池不丢。这才是真正的持续运作。
set -u

FORGE_ROOT="/work/liuyuanjie/forge"
PY="/work/liuyuanjie/envs/vllm-cu128/bin/python"
LOG="/work/liuyuanjie/oml_v4.log"
STOP="/work/liuyuanjie/design_daemon_oml.stop"
OUT="/work/liuyuanjie/forge/artifacts/oml_daemon_trend_v4.jsonl"
CKPT="/work/liuyuanjie/forge/artifacts/oml_daemon_v4_ckpt.pkl"

echo "[supervisor $(date +%FT%T)] 启动, forge_root=$FORGE_ROOT"
rm -f "$STOP"
RESTARTS=0
while true; do
    if [ -f "$STOP" ]; then
        echo "[supervisor $(date +%FT%T)] 检测停止文件, 退出"
        break
    fi
    # 若已在跑(残留), 跳过
    if pgrep -f "design_daemon_oml.py" > /dev/null 2>&1; then
        echo "[supervisor $(date +%FT%T)] 已有进程在跑, 等待..."
        while pgrep -f "design_daemon_oml.py" > /dev/null 2>&1; do
            [ -f "$STOP" ] && echo "[supervisor] 停止" && exit 0
            sleep 50
        done
        continue
    fi
    echo "[supervisor $(date +%FT%T)] 拉起 daemon (restarts=$RESTARTS) ..."
    cd "$FORGE_ROOT"
    export CUDA_VISIBLE_DEVICES=0
    # 前台跑(不是setsid后台): supervisor 可感知崩溃
    "$PY" -u scripts/design_daemon_oml.py \
        --rounds 100000 --forge-root "$FORGE_ROOT" \
        --out "$OUT" --ckpt "$CKPT" --interval 30 \
        >> "$LOG" 2>&1
    EC=$?
    RESTARTS=$((RESTARTS+1))
    echo "[supervisor $(date +%FT%T)] daemon 退出 exit=$EC (restarts=$RESTARTS)"
    if [ -f "$STOP" ]; then
        echo "[supervisor] 检测停止, 退出"
        break
    fi
    if [ "$EC" -eq 2 ]; then
        # exit=2 = 输出文件已存在(防污染门). 这是配置错, 不该无限重启
        echo "[supervisor] FATAL(exit=2): 输出文件已存在, 退出防死循环"
        break
    fi
    sleep 5   # 崩溃后短暂等待再拉起
done
echo "[supervisor $(date +%FT%T)] 结束, 共拉起 $RESTARTS 次"