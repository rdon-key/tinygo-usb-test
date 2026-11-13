#!/usr/bin/env bash
set -euo pipefail

cd /home/user/cdctest

passed=()
failed=()

trap 'echo; echo "Batch interrupted."; exit 130' INT TERM

run() {
    local test="$1"
    local sched="$2"
    local count="$3"
    shift 3

    local name="$test / $sched"
    local arg

    for arg in "$@"; do
        case "$arg" in
            ARGS=*) name+=" (${arg#ARGS=})" ;;
        esac
    done

    echo
    echo "========================================"
    echo " TEST=$test SCHED=$sched COUNT=$count"
    echo "========================================"

    if make test \
        TEST="$test" \
        SCHED="$sched" \
        COUNT="$count" \
        "$@"; then

        echo "OK: $name"
        passed+=("$name")
    else
        local rc=$?
        echo "FAIL: $name (exit=$rc)"
        failed+=("$name (exit=$rc)")
        echo "Continuing to next test..."
    fi
}

batch_start=$(date '+%Y-%m-%d %H:%M:%S')

for test in t3 t4 t5 t7 t8 t9 t10; do
    for sched in tasks cores; do
        case "$test" in
            t3)
                run "$test" "$sched" 10 \
                    INTERVAL=0 \
                    "ARGS=-burst 32 -window 4 -request-interval 0"
                ;;
            t4)
                run "$test" "$sched" 10 \
                    "ARGS=-frames 32"
                ;;
            t5)
                run "$test" "$sched" 10 \
                    "ARGS=-frames 32 -producers 4 -mode raw"

                run "$test" "$sched" 10 \
                    "ARGS=-frames 32 -producers 4 -mode serialized"
                ;;
            t10)
                run "$test" "$sched" 2 \
                    "ARGS=-offsets 20ms,50ms,100ms -off 750ms -reconnect 15s"
                ;;
            *)
                run "$test" "$sched" 2
                ;;
        esac
    done
done

batch_end=$(date '+%Y-%m-%d %H:%M:%S')

echo
echo "========================================"
echo " BATCH TEST SUMMARY"
echo "========================================"
echo "Start  : $batch_start"
echo "End    : $batch_end"
echo "Total  : $((${#passed[@]} + ${#failed[@]}))"
echo "Passed : ${#passed[@]}"
echo "Failed : ${#failed[@]}"
echo

echo "--- PASSED ---"
if ((${#passed[@]} > 0)); then
    printf '  %s\n' "${passed[@]}"
else
    echo "  None"
fi

echo
echo "--- FAILED ---"
if ((${#failed[@]} > 0)); then
    printf '  %s\n' "${failed[@]}"
else
    echo "  None"
fi

echo "========================================"

if ((${#failed[@]} > 0)); then
    exit 1
fi

exit 0
