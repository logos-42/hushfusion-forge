#!/usr/bin/env bash
# 24h 持续运作包装器 v5 —— 配置推荐器(崩溃自动重启 + checkpoint)。
# 用法: setsid nohup bash scripts/oml_supervisor_v5.sh > /work/liuyuanjie/oml_supervisor_v5.out 2>&1 &
# 停: touch /work/liuyuanjie/design_daemon_oml.stop
set -u

FORGE_ROOT="/work/liuyuanjie/forge"
PY="/work/liuyuanjie/envs/vllm-cu128/bin/python"
LOG="/work/liuyuanjie/oml_v5.log"
STOP="/work/liuyuanjie/design_daemon_oml.stop"
CKPT="/work/liuyuanjie/forge/artifacts/oml_daemon_v5_ckpt.pkl"

echo "[supervisor-v5 $(date +%FT%T)] 启动"
rm -f "$STOP"
RESTARTS=0
while true; do
    if [ -f "$STOP" ]; then
        echo "[supervisor-v5] 停止文件, 退出"; break
    fi
    if pgrep -f "design_daemon_config_v5.py" > /dev/null 2>&1; then
        while pgrep -f "design_daemon_config_v5.py" > /dev/null 2>&1; do
            [ -f "$STOP" ] && exit 0; sleep 50
        done
        continue
    fi
    echo "[supervisor-v5] 拉起 daemon (restarts=$RESTARTS)"
    cd "$FORGE_ROOT"
    export CUDA_VISIBLE_DEVICES=0
    TS=$(date +%s)
    OUT="/work/liuyuanjie/forge/artifacts/oml_daemon_trend_v5_${TS}.jsonl"
    "$PY" -u scripts/design_daemon_config_v5.py \
        --rounds 100000 --forge-root "$FORGE_ROOT" \
        --out "$OUT" --ckpt "$CKPT" --interval 30 \
        >> "$LOG" 2>&1
    EC=$?
    RESTARTS=$((RESTARTS+1))
    echo "[supervisor-v5] daemon 退出 exit=$EC (restarts=$RESTARTS)"
    [ -f "$STOP" ] && break
    sleep 5
    # ── 定时归档快照(24h不间断的长期成果可见) ──
    "$PY" scripts/oml_daily_report_v5.py \
        --trend "$OUT" --kb "/work/liuyuanjie/forge/runs/v5_knowledge/registry.jsonl" \
        --out-dir "/work/liuyuanjie/forge/artifacts/daily" \
        >> /work/liuyuanjie/oml_daily_v5.log 2>&1 || true
done
echo "[supervisor-v5] 结束, 共拉起 $RESTARTS 次"