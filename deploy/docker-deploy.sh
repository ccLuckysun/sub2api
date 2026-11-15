#!/bin/bash
# =============================================================================
# Sub2API Docker Deployment Preparation Script
# =============================================================================
# This script prepares deployment files for Sub2API:
#   - Downloads docker-compose.local.yml and .env.example
#   - Generates secure secrets (JWT_SECRET, TOTP_ENCRYPTION_KEY, POSTGRES_PASSWORD)
#   - Generates a random admin login email (ADMIN_EMAIL)
#   - Creates necessary data directories
#
# After running this script, you can start services with:
#   docker-compose up -d
# =============================================================================

set -e
umask 077

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# GitHub raw content base URL
GITHUB_RAW_URL="${GITHUB_RAW_URL:-https://raw.githubusercontent.com/Wei-Shaw/sub2api/main/deploy}"
SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"
START=false
REUSE=false
OVERWRITE=false
DEPLOY_ARGS=()
while [ "$#" -gt 0 ]; do
    case "$1" in
        --monitoring|--host-metrics|--backup)
            START=true; DEPLOY_ARGS+=("$1") ;;
        --start) START=true ;;
        --no-start) START=true; DEPLOY_ARGS+=(--no-start) ;;
        --image)
            [ "$#" -ge 2 ] || { echo '--image requires a value' >&2; exit 1; }
            DEPLOY_ARGS+=(--image "$2"); shift ;;
        --help|-h)
            echo 'Usage: docker-deploy.sh [--start] [--monitoring] [--host-metrics] [--backup] [--image IMAGE:TAG] [--no-start]'
            echo 'No options: prepare the original three-service deployment.'
            echo '--monitoring: prepare and start all five core services, preserving existing credentials.'
            exit 0 ;;
        *) echo "Unknown option: $1" >&2; exit 1 ;;
    esac
    shift
done

# Prefer this checkout when invoked locally. Downloaded installers use the same
# asset URL/version as the existing Compose and env templates.
fetch_asset() {
    local source="$1" target="$2"
    mkdir -p "$(dirname "$target")"
    if [ -f "$SOURCE_DIR/$source" ]; then
        if [ "$SOURCE_DIR/$source" != "$(pwd)/$target" ]; then cp "$SOURCE_DIR/$source" "$target"; fi
    elif command -v curl >/dev/null 2>&1; then
        curl -fsSL "${GITHUB_RAW_URL}/$source" -o "$target"
    elif command -v wget >/dev/null 2>&1; then
        wget -q "${GITHUB_RAW_URL}/$source" -O "$target"
    else
        echo 'curl or wget is required' >&2; return 1
    fi
}

prepare_compose_helper() {
    local asset
    for asset in compose-deploy.sh docker-compose.monitoring.yml monitoring.env.example \
        monitoring/render-prometheus-config.sh monitoring/prometheus.yml.tmpl \
        monitoring/prometheus/rules/slo.yml monitoring/prometheus/rules/chain.yml \
        monitoring/prometheus/rules/infra.yml \
        monitoring/alertmanager/alertmanager.yml.tmpl \
        monitoring/alertmanager/templates/email.tmpl \
        monitoring/sql/grafana-ro.sql \
        monitoring/backup/snapshot.sh monitoring/backup/crontab.example \
        monitoring/grafana/provisioning/datasources/prometheus.yml \
        monitoring/grafana/provisioning/datasources/postgres.yml \
        monitoring/grafana/provisioning/dashboards/dashboards.yml \
        monitoring/grafana/dashboards/01-overview.json \
        monitoring/grafana/dashboards/02-chain.json \
        monitoring/grafana/dashboards/03-nodes.json \
        monitoring/grafana/dashboards/04-users.json \
        monitoring/grafana/dashboards/05-host.json; do
        fetch_asset "$asset" "$asset"
    done
    chmod +x compose-deploy.sh monitoring/render-prometheus-config.sh monitoring/backup/snapshot.sh
}

# Print colored message
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Generate random secret
generate_secret() {
    openssl rand -hex 32
}

# Generate a random admin login email so new installs never use a guessable default
generate_admin_email() {
    local suffix
    suffix=$(openssl rand -hex 6) || return 1
    [ -n "$suffix" ] || return 1
    echo "admin-${suffix}@sub2api.local"
}

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Main installation function
main() {
    echo ""
    echo "=========================================="
    echo "  Sub2API Deployment Preparation"
    echo "=========================================="
    echo ""

    # Check if openssl is available
    if ! command_exists openssl; then
        print_error "openssl is not installed. Please install openssl first."
        exit 1
    fi

    # Check if deployment already exists
    if [ -f "docker-compose.yml" ] && [ -f ".env" ]; then
        if $START; then
            REUSE=true
            print_info "Using existing Compose configuration and .env credentials."
        else
            print_warning "Deployment files already exist in current directory."
            read -p "Overwrite existing files? (y/N): " -r
            echo
            if [[ ! $REPLY =~ ^[Yy]$ ]]; then
                print_info "Cancelled."
                exit 0
            fi
            OVERWRITE=true
        fi
    fi

    if ! $REUSE; then
        # Download docker-compose.local.yml and save as docker-compose.yml
        print_info "Downloading docker-compose.yml..."
        if ! $START || [ ! -f docker-compose.yml ]; then
            fetch_asset docker-compose.local.yml docker-compose.yml
        fi
        print_success "Downloaded docker-compose.yml"

        # Download .env.example
        print_info "Downloading .env.example..."
        fetch_asset .env.example .env.example
        print_success "Downloaded .env.example"

        # Generate .env file with auto-generated secrets
        print_info "Generating secure secrets..."
        echo ""

        # Existing credentials also survive partially prepared deployments.
        if [ ! -f .env ] || $OVERWRITE; then
            # Generate secrets
            JWT_SECRET=$(generate_secret)
            TOTP_ENCRYPTION_KEY=$(generate_secret)
            POSTGRES_PASSWORD=$(generate_secret)
            ADMIN_EMAIL=$(generate_admin_email)

            # Create .env from .env.example
            cp .env.example .env

            # Update .env with generated secrets (cross-platform compatible)
            if sed --version >/dev/null 2>&1; then
                # GNU sed (Linux)
                sed -i "s/^JWT_SECRET=.*/JWT_SECRET=${JWT_SECRET}/" .env
                sed -i "s/^TOTP_ENCRYPTION_KEY=.*/TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}/" .env
                sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${POSTGRES_PASSWORD}/" .env
                sed -i "s/^ADMIN_EMAIL=.*/ADMIN_EMAIL=${ADMIN_EMAIL}/" .env
            else
                # BSD sed (macOS)
                sed -i '' "s/^JWT_SECRET=.*/JWT_SECRET=${JWT_SECRET}/" .env
                sed -i '' "s/^TOTP_ENCRYPTION_KEY=.*/TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}/" .env
                sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${POSTGRES_PASSWORD}/" .env
                sed -i '' "s/^ADMIN_EMAIL=.*/ADMIN_EMAIL=${ADMIN_EMAIL}/" .env
            fi

        fi # generate credentials
    fi # keep existing Compose and credentials during --start/--monitoring upgrades

    # Create data directories
    print_info "Creating data directories..."
    mkdir -p data postgres_data redis_data
    print_success "Created data directories"

    # Set secure permissions for .env file (readable/writable only by owner)
    chmod 600 .env
    echo ""

    if $START; then
        prepare_compose_helper
        ./compose-deploy.sh "${DEPLOY_ARGS[@]}"
        return
    fi

    # Display completion message
    echo "=========================================="
    echo "  Preparation Complete!"
    echo "=========================================="
    echo ""
    echo "Generated secure credentials:"
    echo "  POSTGRES_PASSWORD:     ${POSTGRES_PASSWORD}"
    echo "  JWT_SECRET:            ${JWT_SECRET}"
    echo "  TOTP_ENCRYPTION_KEY:   ${TOTP_ENCRYPTION_KEY}"
    echo "  ADMIN_EMAIL:           ${ADMIN_EMAIL}  (admin login username)"
    echo ""
    print_warning "These credentials have been saved to .env file."
    print_warning "Please keep them secure and do not share publicly!"
    echo ""
    echo "Directory structure:"
    echo "  docker-compose.yml        - Docker Compose configuration"
    echo "  .env                      - Environment variables (generated secrets)"
    echo "  .env.example              - Example template (for reference)"
    echo "  data/                     - Application data (will be created on first run)"
    echo "  postgres_data/            - PostgreSQL data"
    echo "  redis_data/               - Redis data"
    echo ""
    echo "Next steps:"
    echo "  1. (Optional) Edit .env to customize configuration"
    echo "  2. Start services:"
    echo "     docker-compose up -d"
    echo ""
    echo "  3. View logs:"
    echo "     docker-compose logs -f sub2api"
    echo ""
    echo "  4. Access Web UI:"
    echo "     http://localhost:8080"
    echo ""
    print_info "If admin password is not set in .env, it will be auto-generated."
    print_info "Check logs for the generated admin password on first startup."
    echo ""
}

# Run main function
main "$@"
