#!/usr/bin/env bash
# Interview-Agent 一键开发启动 — backend (Go/Gin :9001) + frontend (Vite :5173) + Electron
# 用法: ./dev.sh
# 选项:
#   --no-backend    只起 frontend + electron
#   --no-frontend   只起 backend (电子壳也会跳过, 没前端可加载)
#   --no-electron   不启动 Electron, 只在浏览器里跑
#   --fresh         强制重新拉依赖 (go mod download + pnpm install)

set -e

cd "$(dirname "$0")"

WITH_BACKEND=1
WITH_FRONTEND=1
WITH_ELECTRON=1
FRESH=0
BACKEND_PORT=9001
FRONTEND_PORT=5173
LOG_DIR="logs/dev"

for arg in "$@"; do
  case "$arg" in
    --no-frontend) WITH_FRONTEND=0; WITH_ELECTRON=0 ;;
    --no-backend)  WITH_BACKEND=0 ;;
    --no-electron) WITH_ELECTRON=0 ;;
    --fresh)       FRESH=1 ;;
    -h|--help)
      sed -n '2,9p' "$0"
      exit 0 ;;
    *)
      echo "未知参数: $arg (用 --help 看用法)"
      exit 1 ;;
  esac
done

echo "🎬 启动 Interview-Agent 开发环境"
echo ""
mkdir -p "$LOG_DIR"

PIDS=()

port_pids() {
  lsof -nP -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null || true
}

port_in_use() {
  [ -n "$(port_pids "$1")" ]
}

tcp_ready() {
  nc -z 127.0.0.1 "$1" >/dev/null 2>&1
}

cleanup() {
  echo ""
  echo "⏹  停止所有服务..."
  for pid in "${PIDS[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
  sleep 0.3
  for pid in "${PIDS[@]}"; do
    kill -9 "$pid" 2>/dev/null || true
  done
  exit
}
trap cleanup INT TERM

# ── Backend (Go / Gin) ────────────────────────────────────────────────
if [ "$WITH_BACKEND" = "1" ]; then
  echo "🐹 启动 backend (Go/Gin, 端口 ${BACKEND_PORT})..."

  if port_in_use "$BACKEND_PORT"; then
    echo "  ✗ 端口 ${BACKEND_PORT} 已被占用"
    echo "    占用 PID: $(port_pids "$BACKEND_PORT" | tr '\n' ' ')"
    echo "    可手动杀掉: kill \$(lsof -nP -tiTCP:${BACKEND_PORT} -sTCP:LISTEN)"
    cleanup
  fi

  if [ ! -f ".env" ]; then
    if [ -f ".env.example" ]; then
      echo "  → .env 不存在, 从 .env.example 复制 (记得填 ANTHROPIC_API_KEY)"
      cp .env.example .env
    else
      echo "  ⚠ .env 和 .env.example 都不存在, backend 起不来"
      cleanup
    fi
  fi

  if [ "$FRESH" = "1" ] || [ ! -d "$HOME/go/pkg/mod/cache/download" ]; then
    echo "  → 拉 Go 依赖 (go mod download)..."
    go mod download
  fi

  go run ./cmd/api > "${LOG_DIR}/backend.log" 2>&1 &
  BACKEND_PID=$!
  PIDS+=("$BACKEND_PID")

  echo "  ⏳ 等待 127.0.0.1:${BACKEND_PORT} ..."
  for i in $(seq 1 120); do
    if tcp_ready "$BACKEND_PORT"; then
      echo "  ✓ backend 就绪"
      break
    fi
    if ! kill -0 "$BACKEND_PID" 2>/dev/null; then
      echo "  ✗ backend 进程已退出, 看 ${LOG_DIR}/backend.log 排查"
      tail -20 "${LOG_DIR}/backend.log" | sed 's/^/    /'
      cleanup
    fi
    sleep 0.5
    if [ "$i" = "120" ]; then
      echo "  ⚠ 60s 仍未就绪, 看 ${LOG_DIR}/backend.log 排查"
    fi
  done
fi

# ── Frontend (Vite) ───────────────────────────────────────────────────
if [ "$WITH_FRONTEND" = "1" ]; then
  echo "🎨 启动 frontend (Vite, 固定端口 ${FRONTEND_PORT})..."

  FRONTEND_REUSED=0
  if curl -sf "http://localhost:${FRONTEND_PORT}" >/dev/null 2>&1; then
    echo "  ✓ http://localhost:${FRONTEND_PORT} 已有前端服务, 直接复用"
    FRONTEND_REUSED=1
  elif port_in_use "$FRONTEND_PORT"; then
    echo "  ✗ 端口 ${FRONTEND_PORT} 已被占用, 但不是可访问的前端服务"
    echo "    占用 PID: $(port_pids "$FRONTEND_PORT" | tr '\n' ' ')"
    cleanup
  fi

  if [ "$FRONTEND_REUSED" = "0" ]; then
    pushd web > /dev/null
    if [ "$FRESH" = "1" ] || [ ! -d "node_modules" ]; then
      echo "  → 拉前端依赖 (pnpm install)..."
      pnpm install --silent
    fi
    pnpm dev > "../${LOG_DIR}/frontend.log" 2>&1 &
    FRONTEND_PID=$!
    PIDS+=("$FRONTEND_PID")
    popd > /dev/null

    echo "  ⏳ 等待 http://localhost:${FRONTEND_PORT} ..."
    for i in $(seq 1 60); do
      if curl -sf "http://localhost:${FRONTEND_PORT}" >/dev/null 2>&1; then
        echo "  ✓ frontend 就绪"
        break
      fi
      if ! kill -0 "$FRONTEND_PID" 2>/dev/null; then
        echo "  ✗ frontend 进程已退出, 看 ${LOG_DIR}/frontend.log 排查"
        tail -20 "${LOG_DIR}/frontend.log" | sed 's/^/    /'
        cleanup
      fi
      sleep 0.5
      if [ "$i" = "60" ]; then
        echo "  ⚠ 30s 仍未就绪, 看 ${LOG_DIR}/frontend.log 排查"
      fi
    done
  fi
fi

echo ""
echo "✅ Interview-Agent 已启动"
echo ""
if [ "$WITH_BACKEND" = "1" ]; then
  echo "  🐹 backend  : http://localhost:${BACKEND_PORT}"
fi
if [ "$WITH_FRONTEND" = "1" ]; then
  echo "  🎨 frontend : http://localhost:${FRONTEND_PORT}"
fi

# ── Electron (dev shell) ────────────────────────────────────────────────
# Waits until the Vite dev server actually answers before launching so the
# window doesn't open on a white "can't connect" screen. Bounded to ~20 s.
# Skip via --no-electron; also auto-skipped when --no-frontend is set.
if [ "$WITH_ELECTRON" = "1" ]; then
  echo "🪟 启动 Electron 桌面壳..."

  if [ "$FRESH" = "1" ] || [ ! -d "electron/node_modules" ]; then
    echo "  → 拉 Electron 依赖 (pnpm install, 首次可能要几十秒)..."
    pushd electron > /dev/null
    pnpm install --silent
    popd > /dev/null
  fi

  echo "  ⏳ 等待前端 http://localhost:${FRONTEND_PORT} ..."
  for i in $(seq 1 40); do
    if curl -sf "http://localhost:${FRONTEND_PORT}" >/dev/null 2>&1; then
      break
    fi
    sleep 0.5
    if [ "$i" = "40" ]; then
      echo "  ⚠ 20s 前端仍未响应, 仍尝试启动 Electron (窗口可能白屏)"
    fi
  done

  pushd electron > /dev/null
  pnpm start > "../${LOG_DIR}/electron.log" 2>&1 &
  ELECTRON_PID=$!
  PIDS+=("$ELECTRON_PID")
  popd > /dev/null

  sleep 1
  if ! kill -0 "$ELECTRON_PID" 2>/dev/null; then
    echo "  ✗ Electron 进程已退出, 看 ${LOG_DIR}/electron.log 排查"
    tail -20 "${LOG_DIR}/electron.log" | sed 's/^/    /'
    cleanup
  fi
  echo "  🪟 electron  : 已启动 (INTERVIEW_ELECTRON_DEVTOOLS=1 可自动打开 DevTools)"
fi

echo ""
if [ "$WITH_ELECTRON" = "1" ]; then
  echo "  🪵 日志目录 : ${LOG_DIR}/ (backend.log / frontend.log / electron.log)"
else
  echo "  🪵 日志目录 : ${LOG_DIR}/ (backend.log / frontend.log)"
fi
echo "  按 Ctrl+C 停止所有服务"
echo ""

wait
