#!/usr/bin/env bash
set -euo pipefail

# === Global Variables ===
PORT_FWD_PID=""
CLEANUP_POD_EXISTS=false
NAMESPACE="llama-cpp"
DEPLOYMENT="llama-cpp"
CONFIGMAP="llama-cpp-config"
SERVICE="llama-cpp"
LOCAL_PORT=8080
LOG_FILE=$(mktemp)

MODEL_URL=""
MODEL_FILENAME=""
MMPROJ_URL=""
MMPROJ_FILENAME=""
TIMEOUT=600

# Result variables
STATUS=""
MEDIAN_TOKS=""
TOOL_CALLING=""
VISION=""
VRAM_USAGE=""
EXIT_CODE=0

# === Color Output Helpers ===
info()    { printf '\033[0;34m[INFO]\033[0m %s\n' "$*"; }
warn()    { printf '\033[0;33m[WARN]\033[0m %s\n' "$*"; }
error()   { printf '\033[0;31m[ERROR]\033[0m %s\n' "$*"; }
success() { printf '\033[0;32m[OK]\033[0m %s\n' "$*"; }
phase()   { printf '\n\033[1;36m=== PHASE: %s ===\033[0m\n\n' "$*"; }

# === Usage ===
usage() {
  cat <<'EOF'
test-model.sh — Automated llama.cpp model testing for homelab-cluster

Swaps the model in the llama-cpp deployment, benchmarks it, and documents
results in docs/llama-cpp-model-testing.md.

USAGE:
  ./test-model.sh --model-url <URL> --model-file <FILENAME> [OPTIONS]

REQUIRED:
  --model-url   <URL>       HuggingFace GGUF download URL
  --model-file  <FILENAME>  GGUF filename (e.g., Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf)

OPTIONAL:
  --mmproj-url  <URL>       Vision projector (mmproj) download URL
  --mmproj-file <FILENAME>  Vision projector filename (e.g., mmproj-F16.gguf)
  --timeout     <SECONDS>   Max wait for pod startup (default: 600)
  --help                    Show this help message

EXAMPLES:
  # Text-only model
  ./test-model.sh \
    --model-url "https://huggingface.co/bartowski/Hermes-3-Llama-3.1-8B-GGUF/resolve/main/Hermes-3-Llama-3.1-8B-Q4_K_M.gguf" \
    --model-file "Hermes-3-Llama-3.1-8B-Q4_K_M.gguf"

  # Vision model (with mmproj)
  ./test-model.sh \
    --model-url "https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main/gemma-3-4b-it-Q4_K_M.gguf" \
    --model-file "gemma-3-4b-it-Q4_K_M.gguf" \
    --mmproj-url "https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main/mmproj-gemma-3-4b-it-F16.gguf" \
    --mmproj-file "mmproj-gemma-3-4b-it-F16.gguf"

EXIT CODES:
  0  Model loaded and working (>= 10 tok/s)
  1  Model failed (OOM, crash, download error)
  2  Model loaded but too slow (< 10 tok/s)
EOF
}

# === Cleanup Trap ===
cleanup() {
  echo ""
  info "Cleaning up..."
  if [[ -n "$PORT_FWD_PID" ]] && kill -0 "$PORT_FWD_PID" 2>/dev/null; then
    kill "$PORT_FWD_PID" 2>/dev/null || true
    wait "$PORT_FWD_PID" 2>/dev/null || true
    info "Stopped port-forward (PID $PORT_FWD_PID)"
  fi
  if [[ "$CLEANUP_POD_EXISTS" == true ]]; then
    kubectl delete pod llama-pvc-cleanup -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
    info "Deleted cleanup pod"
  fi
  if [[ -f "$LOG_FILE" ]]; then
    rm -f "$LOG_FILE"
  fi
  info "Cleanup complete"
}
trap cleanup EXIT INT TERM

# === Argument Parsing ===
while [[ $# -gt 0 ]]; do
  case "$1" in
    --model-url)   MODEL_URL="$2";      shift 2 ;;
    --model-file)  MODEL_FILENAME="$2";  shift 2 ;;
    --mmproj-url)  MMPROJ_URL="$2";     shift 2 ;;
    --mmproj-file) MMPROJ_FILENAME="$2"; shift 2 ;;
    --timeout)     TIMEOUT="$2";         shift 2 ;;
    --help)        usage; exit 0 ;;
    *)
      error "Unknown argument: $1"
      usage
      exit 1
      ;;
  esac
done

# === Input Validation ===
if [[ -z "$MODEL_URL" ]]; then
  error "Missing required argument: --model-url"
  echo ""
  usage
  exit 1
fi
if [[ -z "$MODEL_FILENAME" ]]; then
  error "Missing required argument: --model-file"
  echo ""
  usage
  exit 1
fi
if [[ -n "$MMPROJ_URL" && -z "$MMPROJ_FILENAME" ]]; then
  error "--mmproj-url requires --mmproj-file"
  exit 1
fi
if [[ -z "$MMPROJ_URL" && -n "$MMPROJ_FILENAME" ]]; then
  error "--mmproj-file requires --mmproj-url"
  exit 1
fi

# Append ?download=true to HuggingFace URLs for xet storage compatibility
append_hf_download() {
  local url="$1"
  if [[ "$url" == *"huggingface.co"* && "$url" != *"download=true"* ]]; then
    if [[ "$url" == *"?"* ]]; then
      echo "${url}&download=true"
    else
      echo "${url}?download=true"
    fi
  else
    echo "$url"
  fi
}
MODEL_URL=$(append_hf_download "$MODEL_URL")
if [[ -n "$MMPROJ_URL" ]]; then
  MMPROJ_URL=$(append_hf_download "$MMPROJ_URL")
fi

IS_VISION=false
if [[ -n "$MMPROJ_URL" ]]; then
  IS_VISION=true
fi

info "Model:    $MODEL_FILENAME"
info "URL:      $MODEL_URL"
if [[ "$IS_VISION" == true ]]; then
  info "Mmproj:   $MMPROJ_FILENAME"
  info "Mmproj URL: $MMPROJ_URL"
fi
info "Timeout:  ${TIMEOUT}s"
echo ""

# =========================================================================
# PHASE 1: MODEL SWAP
# =========================================================================
phase "MODEL SWAP"

# --- 2.1: Scale down ---
info "Scaling deployment to 0 replicas..."
kubectl scale deployment "$DEPLOYMENT" -n "$NAMESPACE" --replicas=0
info "Waiting for pods to terminate..."
kubectl wait --for=delete pod -l app=llama-cpp -n "$NAMESPACE" --timeout=120s 2>/dev/null || true
success "All llama-cpp pods terminated"

# --- 2.2: PVC cleanup ---
info "Running cleanup pod to delete old models from PVC..."
# Delete any leftover cleanup pod from a previous failed run
kubectl delete pod llama-pvc-cleanup -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
CLEANUP_POD_EXISTS=true
kubectl run llama-pvc-cleanup \
  -n "$NAMESPACE" \
  --image=busybox:latest \
  --restart=Never \
  --override-type=strategic \
  --overrides='{
    "spec": {
      "nodeSelector": {"gpu.node/type": "amd-vulkan"},
      "tolerations": [{"key": "gpu", "operator": "Equal", "value": "amd", "effect": "NoSchedule"}],
      "securityContext": {"fsGroup": 1001},
      "containers": [{
        "name": "llama-pvc-cleanup",
        "image": "busybox:latest",
        "command": ["sh", "-c", "echo Before cleanup: && du -sh /models/*.gguf 2>/dev/null || echo No .gguf files found && rm -fv /models/*.gguf && echo After cleanup: && ls -la /models/ && echo Done"],
        "volumeMounts": [{"name": "models", "mountPath": "/models"}],
        "securityContext": {
          "runAsNonRoot": true,
          "runAsUser": 1001,
          "allowPrivilegeEscalation": false,
          "capabilities": {"drop": ["ALL"]},
          "seccompProfile": {"type": "RuntimeDefault"}
        }
      }],
      "volumes": [{"name": "models", "persistentVolumeClaim": {"claimName": "llama-models"}}]
    }
  }'

info "Waiting for cleanup pod to complete..."
CLEANUP_WAIT=0
while [[ $CLEANUP_WAIT -lt 120 ]]; do
  POD_PHASE=$(kubectl get pod llama-pvc-cleanup -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Pending")
  if [[ "$POD_PHASE" == "Succeeded" ]]; then
    break
  elif [[ "$POD_PHASE" == "Failed" ]]; then
    error "Cleanup pod failed"
    kubectl logs llama-pvc-cleanup -n "$NAMESPACE" 2>/dev/null || true
    kubectl delete pod llama-pvc-cleanup -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
    CLEANUP_POD_EXISTS=false
    exit 1
  fi
  sleep 3
  CLEANUP_WAIT=$((CLEANUP_WAIT + 3))
done

kubectl logs llama-pvc-cleanup -n "$NAMESPACE" 2>/dev/null || true
kubectl delete pod llama-pvc-cleanup -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
CLEANUP_POD_EXISTS=false
success "PVC cleanup complete"

# --- 2.3: Patch ConfigMap ---
info "Patching ConfigMap with new model parameters..."
LLAMA_ARGS="--host 0.0.0.0 --port 8080 -ngl 99 -c 4096 --jinja"
if [[ "$IS_VISION" == true ]]; then
  LLAMA_ARGS="$LLAMA_ARGS --mmproj /models/$MMPROJ_FILENAME"
fi

if [[ "$IS_VISION" == true ]]; then
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: $CONFIGMAP
  namespace: $NAMESPACE
data:
  MODEL_URL: "$MODEL_URL"
  MODEL_FILENAME: "$MODEL_FILENAME"
  MMPROJ_URL: "$MMPROJ_URL"
  MMPROJ_FILENAME: "$MMPROJ_FILENAME"
  LLAMA_ARGS: "$LLAMA_ARGS"
EOF
else
  cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: $CONFIGMAP
  namespace: $NAMESPACE
data:
  MODEL_URL: "$MODEL_URL"
  MODEL_FILENAME: "$MODEL_FILENAME"
  LLAMA_ARGS: "$LLAMA_ARGS"
EOF
fi
success "ConfigMap patched"

# --- 2.4: Scale up ---
info "Scaling deployment to 1 replica..."
kubectl scale deployment "$DEPLOYMENT" -n "$NAMESPACE" --replicas=1
success "Deployment scaled to 1"

# --- 2.5: Wait for pod and stream logs ---
info "Waiting for new pod to appear..."
POD_NAME=""
POD_WAIT=0
while [[ $POD_WAIT -lt 60 ]]; do
  POD_NAME=$(kubectl get pods -l app=llama-cpp -n "$NAMESPACE" -o name 2>/dev/null | head -1 || true)
  if [[ -n "$POD_NAME" ]]; then
    POD_NAME="${POD_NAME#pod/}"
    break
  fi
  sleep 5
  POD_WAIT=$((POD_WAIT + 5))
done

if [[ -z "$POD_NAME" ]]; then
  error "No pod appeared within 60 seconds"
  STATUS="Failed"
  MEDIAN_TOKS="N/A"
  TOOL_CALLING="N/A"
  VISION="N/A"
  EXIT_CODE=1
  # Skip to documentation
else
  info "Pod found: $POD_NAME"

  # Wait briefly for container to start before streaming logs
  sleep 5
  info "Streaming pod logs (background)..."
  kubectl logs -f "$POD_NAME" -n "$NAMESPACE" > "$LOG_FILE" 2>&1 &
  LOG_PID=$!

  # --- 2.6: Failure detection loop ---
  info "Monitoring pod startup (timeout: ${TIMEOUT}s)..."
  ELAPSED=0
  while [[ $ELAPSED -lt $TIMEOUT ]]; do
    POD_PHASE=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "Unknown")

    # Check container status
    RESTART_COUNT=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].restartCount}' 2>/dev/null || echo "0")
    WAITING_REASON=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].state.waiting.reason}' 2>/dev/null || echo "")
    TERMINATED_REASON=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].state.terminated.reason}' 2>/dev/null || echo "")
    CONTAINER_READY=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.containerStatuses[0].ready}' 2>/dev/null || echo "false")

    # Check init container status
    INIT_WAITING=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.initContainerStatuses[0].state.waiting.reason}' 2>/dev/null || echo "")
    INIT_TERMINATED=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.initContainerStatuses[0].state.terminated.reason}' 2>/dev/null || echo "")
    INIT_EXIT_CODE=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.initContainerStatuses[0].state.terminated.exitCode}' 2>/dev/null || echo "")

    # Detect failures
    if [[ "$TERMINATED_REASON" == "OOMKilled" ]]; then
      error "Model crashed: OOMKilled"
      kubectl logs "$POD_NAME" -n "$NAMESPACE" --previous 2>/dev/null | tail -20 || true
      STATUS="OOM crash"
      MEDIAN_TOKS="N/A"
      TOOL_CALLING="N/A"
      VISION="N/A"
      EXIT_CODE=1
      break
    fi

    if [[ "$WAITING_REASON" == "CrashLoopBackOff" ]] || [[ "$RESTART_COUNT" -ge 3 ]]; then
      error "Model crashed: CrashLoopBackOff (restarts: $RESTART_COUNT)"
      kubectl logs "$POD_NAME" -n "$NAMESPACE" --previous 2>/dev/null | tail -20 || true
      STATUS="Failed"
      MEDIAN_TOKS="N/A"
      TOOL_CALLING="N/A"
      VISION="N/A"
      EXIT_CODE=1
      break
    fi

    if [[ "$WAITING_REASON" == "ImagePullBackOff" ]] || [[ "$WAITING_REASON" == "ErrImagePull" ]]; then
      error "Image pull failed: $WAITING_REASON"
      STATUS="Failed"
      MEDIAN_TOKS="N/A"
      TOOL_CALLING="N/A"
      VISION="N/A"
      EXIT_CODE=1
      break
    fi

    if [[ "$INIT_TERMINATED" == "Error" ]] || [[ -n "$INIT_EXIT_CODE" && "$INIT_EXIT_CODE" != "0" && "$INIT_EXIT_CODE" != "" ]]; then
      error "Init container (model download) failed"
      kubectl logs "$POD_NAME" -n "$NAMESPACE" -c model-download 2>/dev/null | tail -10 || true
      STATUS="Failed"
      MEDIAN_TOKS="N/A"
      TOOL_CALLING="N/A"
      VISION="N/A"
      EXIT_CODE=1
      break
    fi

    # Check for success
    if [[ "$POD_PHASE" == "Running" && "$CONTAINER_READY" == "true" ]]; then
      success "Pod is running and ready"
      break
    fi

    # Print progress
    if [[ "$POD_PHASE" == "Pending" ]] || [[ "$POD_PHASE" == "Init:0/1" ]]; then
      info "Pod status: $POD_PHASE (init container running — downloading model)... [${ELAPSED}s/${TIMEOUT}s]"
    else
      info "Pod status: $POD_PHASE (restarts: $RESTART_COUNT) [${ELAPSED}s/${TIMEOUT}s]"
    fi

    sleep 10
    ELAPSED=$((ELAPSED + 10))
  done

  # Timeout check
  if [[ $ELAPSED -ge $TIMEOUT && -z "$STATUS" ]]; then
    error "Timeout after ${TIMEOUT}s waiting for pod to become ready"
    STATUS="Failed"
    MEDIAN_TOKS="N/A"
    TOOL_CALLING="N/A"
    VISION="N/A"
    EXIT_CODE=1
  fi

  # Kill log streaming
  kill "$LOG_PID" 2>/dev/null || true
  wait "$LOG_PID" 2>/dev/null || true
fi

# =========================================================================
# PHASE 2: BENCHMARKING (skip if model failed)
# =========================================================================
if [[ -z "$STATUS" ]]; then
  phase "BENCHMARKING"

  # --- 3.1: Port forward ---
  info "Starting port-forward to $SERVICE..."
  kubectl port-forward "svc/$SERVICE" -n "$NAMESPACE" "$LOCAL_PORT:8080" >/dev/null 2>&1 &
  PORT_FWD_PID=$!
  sleep 2

  # --- 3.2: Health check ---
  info "Waiting for API health check..."
  HEALTH_WAIT=0
  while [[ $HEALTH_WAIT -lt 120 ]]; do
    if curl -sf "http://localhost:$LOCAL_PORT/health" >/dev/null 2>&1; then
      success "API is healthy"
      break
    fi
    sleep 5
    HEALTH_WAIT=$((HEALTH_WAIT + 5))
  done
  if [[ $HEALTH_WAIT -ge 120 ]]; then
    error "API health check timeout after 120s"
    STATUS="Failed"
    MEDIAN_TOKS="N/A"
    TOOL_CALLING="N/A"
    VISION="N/A"
    EXIT_CODE=1
  fi
fi

# --- 3.3 + 3.4: Text completion benchmark (3 iterations, median) ---
if [[ -z "$STATUS" ]]; then
  info "Running text completion benchmark (3 iterations)..."
  TOKS_VALUES=()
  for i in 1 2 3; do
    RESPONSE=$(curl -sf "http://localhost:$LOCAL_PORT/completion" \
      -H "Content-Type: application/json" \
      -d '{"prompt": "Write a short paragraph about the history of computing.", "n_predict": 200, "temperature": 0.7}' 2>/dev/null || echo "")

    if [[ -z "$RESPONSE" ]]; then
      warn "Iteration $i: no response from /completion"
      TOKS_VALUES+=("0")
      continue
    fi

    TOKS=$(echo "$RESPONSE" | jq -r '.timings.predicted_per_second // 0 | . * 100 | round / 100' 2>/dev/null || echo "0")
    TOKS_VALUES+=("$TOKS")
    info "  Iteration $i: $TOKS tok/s"
    sleep 2
  done

  # Compute median (sort and pick middle)
  MEDIAN_TOKS=$(printf '%s\n' "${TOKS_VALUES[@]}" | sort -n | sed -n '2p')
  if [[ -z "$MEDIAN_TOKS" ]]; then
    MEDIAN_TOKS="0"
  fi
  success "Median: $MEDIAN_TOKS tok/s"
fi

# --- 3.5: Tool calling test ---
if [[ -z "$STATUS" ]]; then
  info "Testing tool calling support..."
  TOOL_RESPONSE=$(curl -sf "http://localhost:$LOCAL_PORT/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d '{
      "model": "test",
      "messages": [{"role": "user", "content": "What is the current weather in Tokyo, Japan?"}],
      "tools": [{
        "type": "function",
        "function": {
          "name": "get_weather",
          "description": "Get current weather for a location",
          "parameters": {
            "type": "object",
            "properties": {"location": {"type": "string", "description": "City name"}},
            "required": ["location"]
          }
        }
      }]
    }' 2>/dev/null || echo "")

  if [[ -n "$TOOL_RESPONSE" ]]; then
    HAS_TOOL_CALLS=$(echo "$TOOL_RESPONSE" | jq -r 'if (.choices[0].message.tool_calls // [] | length) > 0 then "yes" else "no" end' 2>/dev/null || echo "no")
    if [[ "$HAS_TOOL_CALLS" == "yes" ]]; then
      TOOL_CALLING="Yes"
      success "Tool calling: supported"
    else
      TOOL_CALLING="No"
      info "Tool calling: not supported (no tool_calls in response)"
    fi
  else
    TOOL_CALLING="No"
    warn "Tool calling: test failed (no response)"
  fi
fi

# --- 3.6: Vision test ---
if [[ -z "$STATUS" ]]; then
  if [[ "$IS_VISION" == true ]]; then
    info "Testing vision support..."
    # Minimal 8x8 red PNG encoded as base64
    TEST_IMAGE_B64="iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAADklEQVQI12P4z8BQDwAEgAF/QualzQAAAABJRU5ErkJggg=="

    VISION_RESPONSE=$(curl -sf "http://localhost:$LOCAL_PORT/v1/chat/completions" \
      -H "Content-Type: application/json" \
      -d "{
        \"model\": \"test\",
        \"messages\": [{
          \"role\": \"user\",
          \"content\": [
            {\"type\": \"text\", \"text\": \"Describe this image briefly.\"},
            {\"type\": \"image_url\", \"image_url\": {\"url\": \"data:image/png;base64,$TEST_IMAGE_B64\"}}
          ]
        }]
      }" 2>/dev/null || echo "")

    if [[ -n "$VISION_RESPONSE" ]]; then
      VISION_CONTENT=$(echo "$VISION_RESPONSE" | jq -r 'if .error then "error" elif (.choices[0].message.content // "") != "" then "yes" else "no" end' 2>/dev/null || echo "no")
      if [[ "$VISION_CONTENT" == "yes" ]]; then
        VISION="Yes"
        success "Vision: supported"
      else
        VISION="No"
        info "Vision: not supported"
      fi
    else
      VISION="No"
      warn "Vision: test failed (no response)"
    fi
  else
    VISION="No"
    info "Vision: skipped (no mmproj provided)"
  fi
fi

# --- 3.7: Status determination and summary ---
if [[ -z "$STATUS" ]]; then
  MEDIAN_INT=$(echo "$MEDIAN_TOKS" | cut -d. -f1)
  if [[ "$MEDIAN_INT" -ge 10 ]]; then
    STATUS="Working"
    EXIT_CODE=0
  else
    STATUS="Too slow"
    EXIT_CODE=2
  fi
fi

echo ""
echo "╔══════════════════════════════════════════════════════╗"
echo "║              MODEL BENCHMARK RESULTS                 ║"
echo "╠══════════════════════════════════════════════════════╣"
printf "║  Model:        %-37s ║\n" "$MODEL_FILENAME"
printf "║  tok/s:        %-37s ║\n" "$MEDIAN_TOKS"
printf "║  Tool Calling: %-37s ║\n" "$TOOL_CALLING"
printf "║  Vision:       %-37s ║\n" "$VISION"
printf "║  Status:       %-37s ║\n" "$STATUS"
echo "╚══════════════════════════════════════════════════════╝"
echo ""

# =========================================================================
# PHASE 3: DOCUMENTATION
# =========================================================================
phase "DOCUMENTATION"

DOC_FILE="docs/llama-cpp-model-testing.md"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOC_PATH="$SCRIPT_DIR/$DOC_FILE"

if [[ ! -f "$DOC_PATH" ]]; then
  warn "Documentation file not found: $DOC_PATH"
  warn "Skipping documentation phase"
else
  # --- 4.1: Parse VRAM usage ---
  VRAM_USAGE=$(grep -i -E '(MiB|VRAM|vram|memory)' "$LOG_FILE" 2>/dev/null \
    | grep -i -E '(buffer|cache|weight|total|vulkan)' \
    | head -5 \
    | tr '\n' '; ' \
    || echo "")
  if [[ -z "$VRAM_USAGE" ]]; then
    VRAM_USAGE="N/A"
  fi

  # --- 4.2: Status emoji ---
  case "$STATUS" in
    "Working")   STATUS_EMOJI="✅" ;;
    "Too slow")  STATUS_EMOJI="⚠️" ;;
    "OOM crash") STATUS_EMOJI="❌" ;;
    *)           STATUS_EMOJI="❌" ;;
  esac

  # Build result string
  case "$STATUS" in
    "Working")   RESULT_STR="Loaded successfully. Inference at ${MEDIAN_TOKS} tok/s." ;;
    "Too slow")  RESULT_STR="Loaded successfully but inference slow at ${MEDIAN_TOKS} tok/s." ;;
    "OOM crash") RESULT_STR="OOM crash. Pod terminated with OOMKilled during model loading." ;;
    *)           RESULT_STR="Failed to load. $STATUS" ;;
  esac
  if [[ "$TOOL_CALLING" == "Yes" ]]; then
    RESULT_STR="$RESULT_STR Tool calling supported."
  fi
  if [[ "$VISION" == "Yes" ]]; then
    RESULT_STR="$RESULT_STR Vision supported."
  fi

  # --- 4.3: Count existing entries and derive model name ---
  NEXT_NUM=$(grep -c '^### [0-9]' "$DOC_PATH" 2>/dev/null || echo "0")
  NEXT_NUM=$((NEXT_NUM + 1))

  # Extract display name: strip .gguf, replace last hyphen-separated quant with " — QUANT"
  DISPLAY_NAME=$(echo "$MODEL_FILENAME" | sed 's/\.gguf$//')
  # Try to extract quant level (Q4_K_M, Q6_K, Q8_0, etc.)
  QUANT=$(echo "$DISPLAY_NAME" | grep -oE 'Q[0-9]+_[A-Z0-9_]+$' || echo "")
  if [[ -n "$QUANT" ]]; then
    NAME_PART=$(echo "$DISPLAY_NAME" | sed "s/-\?${QUANT}$//")
    DISPLAY_NAME="$NAME_PART — $QUANT"
  fi

  # Get file size from model download (parse from init container logs)
  MODEL_SIZE=$(kubectl logs "$POD_NAME" -n "$NAMESPACE" -c model-download 2>/dev/null \
    | grep -oE '[0-9]+(\.[0-9]+)?\s*(GB|MB|GiB|MiB)' | tail -1 || echo "")
  if [[ -z "$MODEL_SIZE" ]]; then
    MODEL_SIZE="Unknown"
  fi

  # Derive source from URL (HuggingFace repo path)
  SOURCE=$(echo "$MODEL_URL" | sed -E 's|https://huggingface.co/||; s|/resolve/.*||; s|\?.*||')

  # --- 4.4: Build and insert model section ---
  NEW_SECTION=$(cat <<SECTION

### ${NEXT_NUM}. ${DISPLAY_NAME} ${STATUS_EMOJI}

| Property | Value |
|----------|-------|
| **Source** | \`${SOURCE}\` |
| **Size** | ${MODEL_SIZE} |
| **VRAM Usage** | ${VRAM_USAGE} |
| **Speed** | **~${MEDIAN_TOKS} tok/s** |
| **Tool Calling** | ${TOOL_CALLING} |
| **Vision** | ${VISION} |
| **Result** | ${RESULT_STR} |
SECTION
  )

  # Insert before "## Performance Summary" using awk for reliable multi-line insertion
  TEMP_SECTION=$(mktemp)
  echo "$NEW_SECTION" > "$TEMP_SECTION"
  TEMP_DOC=$(mktemp)
  if grep -q '^## Performance Summary' "$DOC_PATH"; then
    awk -v section_file="$TEMP_SECTION" '
      /^## Performance Summary/ {
        while ((getline line < section_file) > 0) print line
        close(section_file)
        print ""
      }
      { print }
    ' "$DOC_PATH" > "$TEMP_DOC" && mv "$TEMP_DOC" "$DOC_PATH"
    success "Added model entry #$NEXT_NUM to documentation"
  else
    warn "Could not find '## Performance Summary' header — appending to end of file"
    cat "$TEMP_SECTION" >> "$DOC_PATH"
    success "Appended model entry #$NEXT_NUM to end of documentation"
  fi
  rm -f "$TEMP_SECTION" "$TEMP_DOC"

  # --- 4.5: Add row to Performance Summary table ---
  # Build table row
  TOOLS_EMOJI=$( [[ "$TOOL_CALLING" == "Yes" ]] && echo "✅" || echo "❌" )
  VISION_EMOJI=$( [[ "$VISION" == "Yes" ]] && echo "✅" || echo "❌" )
  SPEED_DISPLAY=$( [[ "$MEDIAN_TOKS" == "N/A" ]] && echo "N/A" || echo "**~${MEDIAN_TOKS}**" )
  TABLE_ROW="| ${NAME_PART:-$DISPLAY_NAME} | ${QUANT:-N/A} | ${MODEL_SIZE} | ${SPEED_DISPLAY} | ${TOOLS_EMOJI} | ${VISION_EMOJI} | ${STATUS} |"

  # Find the last table row in the Performance Summary section and append after it
  # Strategy: find the last pipe-delimited row before the next blank line or heading after "## Performance Summary"
  TEMP_DOC=$(mktemp)
  awk -v new_row="$TABLE_ROW" '
    /^## Performance Summary/ { in_summary=1 }
    in_summary && /^\|/ { last_table=NR; last_line=$0 }
    in_summary && last_table && !/^\|/ && !inserted {
      print new_row
      inserted=1
    }
    { print }
    END { if (in_summary && !inserted) print new_row }
  ' "$DOC_PATH" > "$TEMP_DOC" && mv "$TEMP_DOC" "$DOC_PATH" \
    && success "Added row to Performance Summary table" \
    || warn "Could not update Performance Summary table"
  rm -f "$TEMP_DOC"

  # --- 4.6: Final status ---
  success "Test complete: $STATUS. Results saved to $DOC_FILE"
fi

exit "$EXIT_CODE"
