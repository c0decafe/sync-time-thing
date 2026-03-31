{ pkgs, lib, config, ... }:

let
  projectRoot = toString ./.;
  containerInputs = config.lib.getInputs [
    {
      name = "nix2container";
      url = "github:nlewo/nix2container";
      attribute = "containers";
      follows = [ "nixpkgs" ];
    }
  ];
  nix2container = containerInputs.nix2container.packages.${pkgs.stdenv.system};
  appBinary = pkgs.buildGoModule {
    pname = "sync-time-thing";
    version = "0.1.0";
    src = ./.;
    subPackages = [ "./cmd/sync-time-thing" ];
    vendorHash = "sha256-frlKLghAefshZm8X7E7xL4KrUvdBk1B9SySVxuJBLsY=";
    ldflags = [ "-s" "-w" ];
    env = {
      CGO_ENABLED = "0";
    };
  };
  containerRoot = pkgs.buildEnv {
    name = "sync-time-thing-root";
    paths = [ appBinary pkgs.cacert pkgs.tzdata ];
    pathsToLink = [ "/bin" "/etc/ssl/certs" "/usr/share/zoneinfo" ];
  };
  containerData = pkgs.runCommand "sync-time-thing-container-data" { } ''
    mkdir -p "$out/data"
  '';
  containerEtc = pkgs.runCommand "sync-time-thing-container-etc" { } ''
    mkdir -p "$out/etc"
    cat > "$out/etc/passwd" <<'EOF'
root:x:0:0:root:/root:/sbin/nologin
user:x:1000:1000::/data:/sbin/nologin
EOF
    cat > "$out/etc/group" <<'EOF'
root:x:0:
user:x:1000:
EOF
  '';
  containerTmp = pkgs.runCommand "sync-time-thing-container-tmp" { } ''
    mkdir -p "$out/tmp"
  '';
  prodImage = nix2container.nix2container.buildImage {
    name = config.containers.prod.name;
    tag = config.containers.prod.version;
    initializeNixDatabase = false;
    copyToRoot = [
      containerRoot
      containerData
      containerEtc
      containerTmp
    ];
    perms = [
      {
        path = containerData;
        regex = "/data";
        mode = "0755";
        uid = 1000;
        gid = 1000;
        uname = "user";
        gname = "user";
      }
      {
        path = containerTmp;
        regex = "/tmp";
        mode = "1777";
        uid = 0;
        gid = 0;
        uname = "root";
        gname = "root";
      }
    ];
    config = {
      Entrypoint = [ "/bin/sync-time-thing" ];
      User = "1000:1000";
      WorkingDir = "/data";
      Env = [
        "HOME=/data"
        "USER=user"
        "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
        "SYNCTIMETHING_ADMIN_USERNAME=admin"
        "SYNCTIMETHING_DATA_DIR=/data"
        "SYNCTIMETHING_LISTEN_ADDR=:8080"
        "SYNCTIMETHING_SESSION_TTL=24h"
        "SYNCTIMETHING_TIMEZONE=UTC"
      ];
    };
  };
in {
  packages = with pkgs; [
    curl
    git
    go
    jq
    sqlite
    syncthing
  ];

  env = {
    SYNCTIMETHING_LISTEN_ADDR = lib.mkDefault ":8080";
    SYNCTIMETHING_DATA_DIR = lib.mkDefault "${projectRoot}/.devenv/state/data";
    SYNCTIMETHING_TIMEZONE = lib.mkDefault "UTC";
    SYNCTIMETHING_SESSION_TTL = lib.mkDefault "24h";
    SYNCTIMETHING_ADMIN_USERNAME = lib.mkDefault "admin";
    SYNCTIMETHING_DEV_STATE_DIR = lib.mkDefault "${projectRoot}/.devenv/state";
    SYNCTIMETHING_DEV_APP_DATA_DIR = lib.mkDefault "${projectRoot}/.devenv/state/harness-app";
    SYNCTIMETHING_DEV_APP_ADDR = lib.mkDefault "127.0.0.1:18080";
    SYNCTIMETHING_DEV_APP_URL = lib.mkDefault "http://127.0.0.1:18080";
    SYNCTIMETHING_DEV_ADMIN_USERNAME = lib.mkDefault "admin";
    SYNCTIMETHING_DEV_ADMIN_PASSWORD = lib.mkDefault "devenv-admin-password";
    SYNCTIMETHING_DEV_TIMEZONE = lib.mkDefault "UTC";
    SYNCTIMETHING_DEV_ENCRYPTION_KEY = lib.mkDefault "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=";
    SYNCTIMETHING_DEV_SYNCTHING_HOME = lib.mkDefault "${projectRoot}/.devenv/state/harness-syncthing";
    SYNCTIMETHING_DEV_SYNCTHING_USER_HOME = lib.mkDefault "${projectRoot}/.devenv/state/harness-syncthing-home";
    SYNCTIMETHING_DEV_SYNCTHING_URL = lib.mkDefault "http://127.0.0.1:18484";
    SYNCTIMETHING_DEV_SYNCTHING_API_KEY = lib.mkDefault "synctimething-dev-syncthing-key";
  };

  scripts.synctimething-run.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    go run ./cmd/sync-time-thing
  '';

  scripts.synctimething-build.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    go build ./cmd/sync-time-thing
  '';

  scripts.synctimething-app-run.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/app-run.sh
  '';

  scripts.synctimething-app-reset.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/app-reset.sh
  '';

  scripts.synctimething-syncthing-reset.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/syncthing-reset.sh
  '';

  scripts.synctimething-syncthing-run.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/syncthing-run.sh
  '';

  scripts.synctimething-smoke.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/smoke.sh
  '';

  scripts.synctimething-smoke-check.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/smoke-check.sh
  '';

  scripts.synctimething-e2e.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/e2e.sh
  '';

  scripts.synctimething-e2e-check.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    bash ./scripts/e2e-check.sh
  '';

  scripts.synctimething-check.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    synctimething-test
    synctimething-smoke
    synctimething-e2e
  '';

  scripts.synctimething-test.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    go test ./... -coverprofile=coverage.out
    go tool cover -func=coverage.out
  '';

  scripts.synctimething-fmt.exec = ''
    set -euo pipefail
    cd "${projectRoot}"
    gofmt -w $(find cmd internal -name '*.go' -type f)
  '';

  tasks."synctimething:reset-syncthing" = {
    exec = ''
      set -euo pipefail
      cd "${projectRoot}"
      synctimething-syncthing-reset
    '';
    before = [ "devenv:processes:syncthing" ];
  };

  tasks."synctimething:reset-app" = {
    exec = ''
      set -euo pipefail
      cd "${projectRoot}"
      synctimething-app-reset
    '';
    before = [ "devenv:processes:app" ];
  };

  processes.syncthing = {
    exec = "synctimething-syncthing-run";
    cwd = projectRoot;
    ready = {
      exec = ''
        curl -fsS -H "X-API-Key: $SYNCTIMETHING_DEV_SYNCTHING_API_KEY" "$SYNCTIMETHING_DEV_SYNCTHING_URL/rest/system/ping" >/dev/null
      '';
      initial_delay = 1;
      period = 1;
      timeout = 2;
      failure_threshold = 30;
    };
  };

  processes.app = {
    exec = ''
      env \
        SYNCTIMETHING_LISTEN_ADDR="$SYNCTIMETHING_DEV_APP_ADDR" \
        SYNCTIMETHING_DATA_DIR="$SYNCTIMETHING_DEV_APP_DATA_DIR" \
        SYNCTIMETHING_ADMIN_USERNAME="$SYNCTIMETHING_DEV_ADMIN_USERNAME" \
        SYNCTIMETHING_ADMIN_PASSWORD="$SYNCTIMETHING_DEV_ADMIN_PASSWORD" \
        SYNCTIMETHING_TIMEZONE="$SYNCTIMETHING_DEV_TIMEZONE" \
        SYNCTIMETHING_ENCRYPTION_KEY="$SYNCTIMETHING_DEV_ENCRYPTION_KEY" \
        ${appBinary}/bin/sync-time-thing
    '';
    cwd = projectRoot;
    after = [ "devenv:processes:syncthing" ];
    ready = {
      exec = ''
        curl -fsS "$SYNCTIMETHING_DEV_APP_URL/healthz" >/dev/null
      '';
      initial_delay = 1;
      period = 1;
      timeout = 2;
      failure_threshold = 30;
    };
  };

  enterShell = ''
    mkdir -p "$SYNCTIMETHING_DATA_DIR"
    echo "SyncTimeThing devenv ready."
    echo "Run: synctimething-run"
    echo "Processes: devenv processes up"
    echo "Wait for processes: devenv processes wait"
    echo "Harness app: synctimething-app-run"
    echo "Harness app reset: synctimething-app-reset"
    echo "Harness Syncthing: synctimething-syncthing-run"
    echo "Test: synctimething-test"
    echo "Smoke: synctimething-smoke"
    echo "Smoke check only: synctimething-smoke-check"
    echo "E2E: synctimething-e2e"
    echo "E2E check only: synctimething-e2e-check"
    echo "Full check: synctimething-check"
    echo "Build: synctimething-build"
    echo "Container: devenv container build prod"
    echo "Data dir: $SYNCTIMETHING_DATA_DIR"
    echo "Remember to set SYNCTIMETHING_ADMIN_PASSWORD before first boot."
  '';

  enterTest = ''
    synctimething-test
    synctimething-smoke-check
    synctimething-e2e-check
  '';

  containers.prod = {
    name = "sync-time-thing";
    copyToRoot = lib.mkForce containerRoot;
    entrypoint = lib.mkForce [ "/bin/sync-time-thing" ];
    layers = lib.mkForce [ ];
    derivation = lib.mkForce prodImage;
  };
}
