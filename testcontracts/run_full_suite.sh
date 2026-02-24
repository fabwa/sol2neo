#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$(cd "${ROOT_DIR}/.." && pwd)"
SOL2NEO_BIN="${SOL2NEO_BIN:-${WORKSPACE_DIR}/bin/sol2neo}"
NEOGO_BIN="${NEOGO_BIN:-neo-go}"
INTEROP_VERSION="${INTEROP_VERSION:-v0.0.0-20260121113504-979d1f4aada1}"
NEOGO_REPO_REF="${NEOGO_REPO_REF:-979d1f4aada1}"
DEFAULT_NEOGO_REPO="${ROOT_DIR}/.deps/neo-go-master"
NEOGO_REPO="${NEOGO_REPO:-${DEFAULT_NEOGO_REPO}}"

BUILD_ROOT="${ROOT_DIR}/.suite_build"
BASELINE_BUILD_DIR="${BUILD_ROOT}/baseline"
BASELINE_RESULTS="${BASELINE_BUILD_DIR}/results.tsv"
COMPLEX_BUILD_DIR="${BUILD_ROOT}/complex_examples"
COMPLEX_RESULTS_NEOGO="${COMPLEX_BUILD_DIR}/results.neogo.tsv"
COMPLEX_RESULTS_GOBUILD="${COMPLEX_BUILD_DIR}/results.gobuild.tsv"
EXTERNAL_BUILD_DIR="${BUILD_ROOT}/external_semantic"
EXTERNAL_RESULTS="${EXTERNAL_BUILD_DIR}/results.tsv"
SEMTEST_BUILD_DIR="${BUILD_ROOT}/solidity_semantictests"
SEMTEST_RESULTS="${SEMTEST_BUILD_DIR}/results.tsv"
FULL_SUMMARY="${ROOT_DIR}/full_suite.results.tsv"

if ! command -v "${NEOGO_BIN}" >/dev/null 2>&1; then
  echo "error: neo-go binary not found in PATH (expected command: ${NEOGO_BIN})" >&2
  exit 1
fi

if [[ ! -d "${NEOGO_REPO}" ]]; then
  mkdir -p "$(dirname "${NEOGO_REPO}")"
  echo "[setup] Cloning neo-go source into ${NEOGO_REPO}..."
  git clone https://github.com/nspcc-dev/neo-go.git "${NEOGO_REPO}" >/dev/null 2>&1
fi

if [[ "${NEOGO_REPO}" == "${DEFAULT_NEOGO_REPO}" ]]; then
  echo "[setup] Pinning neo-go source at ${NEOGO_REPO_REF}..."
  if ! (cd "${NEOGO_REPO}" && git cat-file -e "${NEOGO_REPO_REF}^{commit}" >/dev/null 2>&1); then
    (
      cd "${NEOGO_REPO}"
      git fetch origin >/dev/null 2>&1 || true
    )
  fi
  if ! (cd "${NEOGO_REPO}" && git checkout -q "${NEOGO_REPO_REF}"); then
    echo "error: unable to checkout NEOGO_REPO_REF=${NEOGO_REPO_REF} in ${NEOGO_REPO}" >&2
    exit 1
  fi
fi

rm -rf "${BUILD_ROOT}"
mkdir -p "$(dirname "${SOL2NEO_BIN}")" "${BASELINE_BUILD_DIR}" "${COMPLEX_BUILD_DIR}" "${EXTERNAL_BUILD_DIR}" "${SEMTEST_BUILD_DIR}"

echo -e "suite\tcontract\ttranspile\tgobuild\tneogo" > "${FULL_SUMMARY}"

write_module_file() {
  local out_dir="$1"
  local module_name="$2"
  cat > "${out_dir}/go.mod" <<EOF
module ${module_name}

go 1.24.0

require github.com/nspcc-dev/neo-go/pkg/interop ${INTEROP_VERSION}
EOF

  cat >> "${out_dir}/go.mod" <<EOF

replace github.com/nspcc-dev/neo-go => ${NEOGO_REPO}
EOF
}

write_contract_yaml() {
  local out_dir="$1"
  local contract_name="$2"
  cat > "${out_dir}/${contract_name}.yml" <<EOF
name: "${contract_name}"
sourceurl: ""
supportedstandards: []
events: []
permissions: []
EOF
}

run_suite() {
  local suite_name="$1"
  local source_dir="$2"
  local build_dir="$3"
  local results_with_neogo="$4"
  local results_without_neogo="$5"

  echo -e "contract\ttranspile\tgobuild\tneogo" > "${results_with_neogo}"
  if [[ -n "${results_without_neogo}" ]]; then
    echo -e "contract\ttranspile\tgobuild" > "${results_without_neogo}"
  fi

  local src contract_name out_dir module_name transpile_status gobuild_status neogo_status
  while IFS= read -r src; do
    contract_name="$(basename "${src}" .sol)"
    out_dir="${build_dir}/${contract_name}"
    module_name="$(echo "${contract_name}" | tr '[:upper:]' '[:lower:]')"

    mkdir -p "${out_dir}"

    transpile_status="PASS"
    gobuild_status="SKIP"
    neogo_status="SKIP"

    if ! "${SOL2NEO_BIN}" -i "${src}" -o "${out_dir}/${contract_name}.go" -v > "${out_dir}/transpile.log" 2>&1; then
      transpile_status="FAIL"
    fi

    if [[ "${transpile_status}" == "PASS" ]]; then
      write_module_file "${out_dir}" "${module_name}"
      write_contract_yaml "${out_dir}" "${contract_name}"

      if ! (cd "${out_dir}" && GO111MODULE=on go mod tidy > gomod.log 2>&1); then
        :
      fi

      if (cd "${out_dir}" && GO111MODULE=on go build . > gobuild.log 2>&1); then
        gobuild_status="PASS"
      else
        gobuild_status="FAIL"
      fi

      if [[ "${gobuild_status}" == "PASS" ]]; then
        if (cd "${out_dir}" && "${NEOGO_BIN}" contract compile \
          -i "${contract_name}.go" \
          -o "${contract_name}.nef" \
          -m "${contract_name}.manifest.json" \
          -c "${contract_name}.yml" \
          --no-events \
          --no-permissions > neocompile.log 2>&1); then
          neogo_status="PASS"
        else
          neogo_status="FAIL"
        fi
      fi
    fi

    echo -e "${contract_name}\t${transpile_status}\t${gobuild_status}\t${neogo_status}" >> "${results_with_neogo}"
    if [[ -n "${results_without_neogo}" ]]; then
      echo -e "${contract_name}\t${transpile_status}\t${gobuild_status}" >> "${results_without_neogo}"
    fi
    echo -e "${suite_name}\t${contract_name}\t${transpile_status}\t${gobuild_status}\t${neogo_status}" >> "${FULL_SUMMARY}"
  done < <(find "${source_dir}" -maxdepth 1 -type f -name '*.sol' | sort)
}

echo "[1/5] Building sol2neo..."
if ! (cd "${WORKSPACE_DIR}" && GO111MODULE=on go build -o "${SOL2NEO_BIN}" ./cmd/sol2neo); then
  echo "error: failed to build sol2neo binary" >&2
  exit 1
fi

echo "[2/5] Running baseline suite..."
run_suite "baseline" "${ROOT_DIR}" "${BASELINE_BUILD_DIR}" "${BASELINE_RESULTS}" ""

echo "[3/5] Running complex examples suite..."
run_suite "complex_examples" "${ROOT_DIR}/complex_examples" "${COMPLEX_BUILD_DIR}" "${COMPLEX_RESULTS_NEOGO}" "${COMPLEX_RESULTS_GOBUILD}"

echo "[4/5] Running external semantic suite..."
run_suite "external_semantic" "${ROOT_DIR}/external_semantic" "${EXTERNAL_BUILD_DIR}" "${EXTERNAL_RESULTS}" ""

echo "[5/5] Running Solidity semantic smoke suite..."
run_suite "solidity_semantictests" "${ROOT_DIR}/external_semantic/solidity_semantictests" "${SEMTEST_BUILD_DIR}" "${SEMTEST_RESULTS}" ""

echo
echo "Full suite complete."
echo "Baseline: ${BASELINE_RESULTS}"
echo "Complex:  ${COMPLEX_RESULTS_NEOGO}"
echo "External: ${EXTERNAL_RESULTS}"
echo "SemTests: ${SEMTEST_RESULTS}"
echo "Summary:  ${FULL_SUMMARY}"
