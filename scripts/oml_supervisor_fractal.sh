#!/usr/bin/env bash
# 分形循环 24h 长期持续学习 supervisor —— 崩溃自愈 + ckpt + 每小时归档。
# 用法: setsid nohup bash scripts/oml_supervisor_fractal.sh > /work/liuyuanjie/oml_supervisor_fractal.out 2>&1 &
# 停: touch /work/liuyuanjie/design_daemon_fractal.stop
set -u

FORGE_ROOT="/work/liuyuanjie/forge"
PY="/work/liuyuanjie/envs/vllm-cu128/bin/python"
LOG="/work/liuyuanjie/oml_fractal.log"
STOP="/work/liuyuanjie/design_daemon_fractal.stop"
CKPT="/work/liuyuanjie/forge/artifacts/oml_daemon_fractal_ckpt.pkl"

echo "[supervisor-fractal $(date +%FT%T)] 启动"
rm -f "$STOP"
RESTARTS=0
while true; do
    if [ -f "$STOP" ]; then
        echo "[supervisor-fractal] 停止文件, 退出"; break
    fi
    if pgrep -f "design_daemon_fractal.py" > /dev/null 2>&1; then
        while pgrep -f "design_daemon_fractal.py" > /dev/null 2>&1; do
            [ -f "$STOP" ] && exit 0; sleep 50
        done
        continue
    fi
    echo "[supervisor-fractal] 拉起 daemon (restarts=$RESTARTS)"
    cd "$FORGE_ROOT"
    TS=$(date +%s)
    OUT="/work/liuyuanjie/forge/artifacts/oml_daemon_trend_fractal_${TS}.jsonl"
    "$PY" -u scripts/design_daemon_fractal.py \
        --rounds 200000 --out "$OUT" --ckpt "$CKPT" --interval 1.0 \
        >> "$LOG" 2>&1
    EC=$?
    RESTARTS=$((RESTARTS+1))
    echo "[supervisor-fractal] daemon 退出 exit=$EC (restarts=$RESTARTS)"
    [ -f "$STOP" ] && break
    sleep 5
done
echo "[supervisor-fractal] 结束, 共拉起 $RESTARTS 次"