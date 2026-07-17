#!/usr/bin/env bats
# Unit tests for scripts/install-release.sh (OB-062).
#
# These tests exercise the checksum-verify and version-verify fail-closed
# paths, plus the atomic-install happy path and its partial-failure
# behavior, entirely against LOCAL fixtures (a fake binary, a hand-built
# SHA256SUMS, and a fake --version stub). No network call and no real
# GitHub release are involved.
#
# Every function call goes through `run bash -c "source '$SCRIPT'; ..."` so
# each test gets a fresh subshell: the script's own `set -euo pipefail` never
# bleeds into bats' own control flow, and a function that calls `exit`
# (there are none at this layer; only main() exits) would only end that
# subshell, not the test runner.

setup() {
  SCRIPT="${BATS_TEST_DIRNAME}/../scripts/install-release.sh"
  WORK_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$WORK_DIR"
}

# write_fake_binary creates a non-executable script at path that prints
# version when invoked as `path --version`, and fails any other invocation.
# Non-executable on purpose: verify_versions is responsible for chmod +x
# before running it, and this proves that responsibility is exercised.
write_fake_binary() {
  local path="$1" version="$2"
  cat > "$path" <<EOF
#!/usr/bin/env bash
if [[ "\${1:-}" == "--version" ]]; then
  echo "${version}"
  exit 0
fi
echo "unsupported invocation" >&2
exit 1
EOF
}

# write_sums (re)computes real SHA256 hashes for the given filenames inside
# dir and writes a SHA256SUMS file there, matching the Makefile dist target's
# own "cd dist && sha256sum ... > SHA256SUMS" convention.
write_sums() {
  local dir="$1"
  shift
  ( cd "$dir" && sha256sum "$@" > SHA256SUMS )
}

# make_fixture_set builds a valid four-binary + SHA256SUMS scratch directory
# for the given version, all reporting that version via --version.
make_fixture_set() {
  local dir="$1" version="$2"
  local -a names=(openbrain-web openbrain-telegram openbrain-slack openbrain-watchd)
  local -a assets=()
  local name
  for name in "${names[@]}"; do
    local asset="${name}-${version}-linux-amd64"
    write_fake_binary "${dir}/${asset}" "$version"
    assets+=("$asset")
  done
  write_sums "$dir" "${assets[@]}"
}

# --- asset_filename -----------------------------------------------------

@test "asset_filename builds the binary-version-platform name" {
  run bash -c "source '$SCRIPT'; asset_filename openbrain-web v0.7.1"
  [ "$status" -eq 0 ]
  [ "$output" = "openbrain-web-v0.7.1-linux-amd64" ]
}

# --- expected_checksum ----------------------------------------------------

@test "expected_checksum returns the hash for a listed filename" {
  echo hello > "${WORK_DIR}/thing"
  write_sums "$WORK_DIR" thing
  local want
  want="$(sha256sum "${WORK_DIR}/thing" | awk '{print $1}')"

  run bash -c "source '$SCRIPT'; expected_checksum '${WORK_DIR}/SHA256SUMS' thing"
  [ "$status" -eq 0 ]
  [ "$output" = "$want" ]
}

@test "expected_checksum fails closed when the filename is not listed" {
  echo hello > "${WORK_DIR}/thing"
  write_sums "$WORK_DIR" thing

  run bash -c "source '$SCRIPT'; expected_checksum '${WORK_DIR}/SHA256SUMS' missing-thing"
  [ "$status" -ne 0 ]
}

# --- verify_checksums -----------------------------------------------------

@test "verify_checksums succeeds against a valid local fixture (happy path)" {
  make_fixture_set "$WORK_DIR" v9.9.9

  run bash -c "source '$SCRIPT'; verify_checksums '${WORK_DIR}' SHA256SUMS \
    openbrain-web-v9.9.9-linux-amd64 openbrain-telegram-v9.9.9-linux-amd64 \
    openbrain-slack-v9.9.9-linux-amd64 openbrain-watchd-v9.9.9-linux-amd64"
  [ "$status" -eq 0 ]
}

@test "verify_checksums fails closed and names the asset on a single corrupted binary" {
  make_fixture_set "$WORK_DIR" v9.9.9
  echo "corrupted bytes" >> "${WORK_DIR}/openbrain-slack-v9.9.9-linux-amd64"

  run bash -c "source '$SCRIPT'; verify_checksums '${WORK_DIR}' SHA256SUMS \
    openbrain-web-v9.9.9-linux-amd64 openbrain-telegram-v9.9.9-linux-amd64 \
    openbrain-slack-v9.9.9-linux-amd64 openbrain-watchd-v9.9.9-linux-amd64"
  [ "$status" -ne 0 ]
  [[ "$output" == *"checksum"* ]]
  [[ "$output" == *"openbrain-slack-v9.9.9-linux-amd64"* ]]
}

@test "verify_checksums fails closed when SHA256SUMS itself is missing" {
  write_fake_binary "${WORK_DIR}/openbrain-web-v9.9.9-linux-amd64" v9.9.9

  run bash -c "source '$SCRIPT'; verify_checksums '${WORK_DIR}' SHA256SUMS openbrain-web-v9.9.9-linux-amd64"
  [ "$status" -ne 0 ]
}

@test "verify_checksums fails closed when a listed asset was never downloaded" {
  make_fixture_set "$WORK_DIR" v9.9.9
  rm -f "${WORK_DIR}/openbrain-watchd-v9.9.9-linux-amd64"

  run bash -c "source '$SCRIPT'; verify_checksums '${WORK_DIR}' SHA256SUMS \
    openbrain-web-v9.9.9-linux-amd64 openbrain-telegram-v9.9.9-linux-amd64 \
    openbrain-slack-v9.9.9-linux-amd64 openbrain-watchd-v9.9.9-linux-amd64"
  [ "$status" -ne 0 ]
  [[ "$output" == *"openbrain-watchd-v9.9.9-linux-amd64"* ]]
}

# --- verify_versions -------------------------------------------------------

@test "verify_versions succeeds when every binary reports the expected version (happy path)" {
  make_fixture_set "$WORK_DIR" v9.9.9

  run bash -c "source '$SCRIPT'; verify_versions '${WORK_DIR}' v9.9.9 \
    openbrain-web openbrain-telegram openbrain-slack openbrain-watchd"
  [ "$status" -eq 0 ]
}

@test "verify_versions fails closed and names the binary on a version mismatch" {
  local dir="$WORK_DIR"
  write_fake_binary "${dir}/openbrain-web-v9.9.9-linux-amd64" v9.9.9
  write_fake_binary "${dir}/openbrain-telegram-v9.9.9-linux-amd64" v0.0.1
  write_fake_binary "${dir}/openbrain-slack-v9.9.9-linux-amd64" v9.9.9
  write_fake_binary "${dir}/openbrain-watchd-v9.9.9-linux-amd64" v9.9.9

  run bash -c "source '$SCRIPT'; verify_versions '${dir}' v9.9.9 \
    openbrain-web openbrain-telegram openbrain-slack openbrain-watchd"
  [ "$status" -ne 0 ]
  [[ "$output" == *"openbrain-telegram"* ]]
  [[ "$output" == *"v0.0.1"* ]]
}

@test "verify_versions fails closed when a binary cannot be executed" {
  write_fake_binary "${WORK_DIR}/openbrain-web-v9.9.9-linux-amd64" v9.9.9
  # Overwrite with a stub that exits non-zero even for --version.
  cat > "${WORK_DIR}/openbrain-web-v9.9.9-linux-amd64" <<'EOF'
#!/usr/bin/env bash
exit 7
EOF

  run bash -c "source '$SCRIPT'; verify_versions '${WORK_DIR}' v9.9.9 openbrain-web"
  [ "$status" -ne 0 ]
}

# --- atomic_install ---------------------------------------------------------

@test "atomic_install places all four binaries atomically with mode 0755 (happy path)" {
  make_fixture_set "$WORK_DIR" v9.9.9
  local install_dir="${WORK_DIR}/install"
  mkdir -p "$install_dir"

  run bash -c "source '$SCRIPT'; atomic_install '${WORK_DIR}' '${install_dir}' v9.9.9 \
    openbrain-web openbrain-telegram openbrain-slack openbrain-watchd"
  [ "$status" -eq 0 ]

  for name in openbrain-web openbrain-telegram openbrain-slack openbrain-watchd; do
    [ -f "${install_dir}/${name}" ]
    perm="$(stat -c '%a' "${install_dir}/${name}")"
    [ "$perm" = "755" ]
    "${install_dir}/${name}" --version | grep -q v9.9.9
  done

  # No leftover temp dotfiles from the write-to-temp-then-rename step.
  run bash -c "shopt -s dotglob nullglob; ls '${install_dir}'"
  [[ "$output" != *".openbrain"* ]]
}

@test "atomic_install is idempotent: re-running the same version succeeds with no error" {
  make_fixture_set "$WORK_DIR" v9.9.9
  local install_dir="${WORK_DIR}/install"
  mkdir -p "$install_dir"

  run bash -c "source '$SCRIPT'; atomic_install '${WORK_DIR}' '${install_dir}' v9.9.9 \
    openbrain-web openbrain-telegram openbrain-slack openbrain-watchd"
  [ "$status" -eq 0 ]

  run bash -c "source '$SCRIPT'; atomic_install '${WORK_DIR}' '${install_dir}' v9.9.9 \
    openbrain-web openbrain-telegram openbrain-slack openbrain-watchd"
  [ "$status" -eq 0 ]
  [ -f "${install_dir}/openbrain-web" ]
}

@test "atomic_install leaves not-yet-processed prior binaries intact when a later source is missing" {
  make_fixture_set "$WORK_DIR" v9.9.9
  local install_dir="${WORK_DIR}/install"
  mkdir -p "$install_dir"

  for name in openbrain-web openbrain-telegram openbrain-slack openbrain-watchd; do
    echo "OLD-${name}" > "${install_dir}/${name}"
    chmod 755 "${install_dir}/${name}"
  done

  # Simulate a mid-run failure: the third binary's verified source vanished.
  rm -f "${WORK_DIR}/openbrain-slack-v9.9.9-linux-amd64"

  run bash -c "source '$SCRIPT'; atomic_install '${WORK_DIR}' '${install_dir}' v9.9.9 \
    openbrain-web openbrain-telegram openbrain-slack openbrain-watchd"
  [ "$status" -ne 0 ]

  # Binaries processed before the failure were updated.
  run cat "${install_dir}/openbrain-web"
  [[ "$output" == v9.9.9* ]] || "${install_dir}/openbrain-web" --version | grep -q v9.9.9

  # The binary that failed to stage, and any not yet reached, keep their
  # prior content untouched: no corrupted/half-written file appears.
  run cat "${install_dir}/openbrain-slack"
  [ "$output" = "OLD-openbrain-slack" ]
  run cat "${install_dir}/openbrain-watchd"
  [ "$output" = "OLD-openbrain-watchd" ]

  # No leftover temp dotfiles.
  run bash -c "shopt -s dotglob nullglob; ls '${install_dir}'"
  [[ "$output" != *".openbrain-slack."* ]]
}

# --- check_prereqs -----------------------------------------------------------

@test "check_prereqs fails when the install directory does not exist" {
  run bash -c "source '$SCRIPT'; check_prereqs '${WORK_DIR}/does-not-exist'"
  [ "$status" -ne 0 ]
}

@test "check_prereqs succeeds when the install directory is writable and a download path exists" {
  local install_dir="${WORK_DIR}/install"
  mkdir -p "$install_dir"
  run bash -c "source '$SCRIPT'; check_prereqs '${install_dir}'"
  [ "$status" -eq 0 ]
}

@test "check_prereqs fails closed when neither gh nor curl is on PATH" {
  local install_dir="${WORK_DIR}/install"
  mkdir -p "$install_dir"

  # A PATH containing only bash itself: `command -v gh` and `command -v curl`
  # both fail to resolve, exercising check_prereqs' own no-download-path
  # branch rather than failing for the unrelated reason of bash being
  # unfindable.
  local minimal_path_dir="${WORK_DIR}/minimal-path"
  mkdir -p "$minimal_path_dir"
  ln -s "$(command -v bash)" "${minimal_path_dir}/bash"

  run env PATH="$minimal_path_dir" bash -c "source '$SCRIPT'; check_prereqs '${install_dir}'"
  [ "$status" -ne 0 ]
  [[ "$output" == *"gh is not installed"* ]]
}

# --- end-to-end local pipeline (no network) ---------------------------------

@test "verify_checksums, verify_versions, and atomic_install compose to a full local install" {
  make_fixture_set "$WORK_DIR" v9.9.9
  local install_dir="${WORK_DIR}/install"
  mkdir -p "$install_dir"

  run bash -c "
    source '$SCRIPT'
    set -e
    verify_checksums '${WORK_DIR}' SHA256SUMS \
      openbrain-web-v9.9.9-linux-amd64 openbrain-telegram-v9.9.9-linux-amd64 \
      openbrain-slack-v9.9.9-linux-amd64 openbrain-watchd-v9.9.9-linux-amd64
    verify_versions '${WORK_DIR}' v9.9.9 \
      openbrain-web openbrain-telegram openbrain-slack openbrain-watchd
    atomic_install '${WORK_DIR}' '${install_dir}' v9.9.9 \
      openbrain-web openbrain-telegram openbrain-slack openbrain-watchd
  "
  [ "$status" -eq 0 ]

  for name in openbrain-web openbrain-telegram openbrain-slack openbrain-watchd; do
    "${install_dir}/${name}" --version | grep -q v9.9.9
  done
}
