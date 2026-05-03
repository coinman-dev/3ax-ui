#!/bin/bash
# coinman-dev/3ax-ui

red='\033[0;31m'
green='\033[0;32m'
blue='\033[0;34m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)

xui_folder="${XUI_MAIN_FOLDER:=/usr/local/x-ui}"
xui_service="${XUI_SERVICE:=/etc/systemd/system}"

# Resolve the directory the script lives in. When the script is piped via
# `bash <(curl ...)` this resolves to /dev/fd/N — that's fine, the local-source
# detector below will simply find no source files and fall back to GitHub.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)" || SCRIPT_DIR=""

# Returns 0 (true) when install.sh is being run from inside a cloned 3ax-ui
# git checkout. The script then builds the panel binary from the local source
# instead of downloading a prebuilt release. Conditions:
#   - BASH_SOURCE points to a real install.sh on disk (rejects curl|bash and
#     bash <(curl ...) flows where BASH_SOURCE is "bash" or /dev/fd/*).
#   - Required source files (main.go, go.mod, web/, .git/) exist next to it.
is_local_source_install() {
    local src_name
    src_name="$(basename "${BASH_SOURCE[0]:-}")"
    [[ "$src_name" == "install.sh" ]] || return 1
    [[ -n "$SCRIPT_DIR" ]] || return 1
    [[ -f "$SCRIPT_DIR/install.sh" ]] || return 1
    [[ -f "$SCRIPT_DIR/main.go" ]] || return 1
    [[ -f "$SCRIPT_DIR/go.mod" ]] || return 1
    [[ -d "$SCRIPT_DIR/web" ]] || return 1
    [[ -d "$SCRIPT_DIR/.git" ]] || return 1
    return 0
}

# Branch to fetch auxiliary files (x-ui.sh, service files) from.
# --beta / --pre → dev branch; otherwise → main
if [[ "$1" == "--beta" || "$1" == "--pre" ]]; then
    REPO_BRANCH="dev"
else
    REPO_BRANCH="main"
fi

# check root
[[ $EUID -ne 0 ]] && echo -e "${red}Fatal error: ${plain} Please run this script with root privilege \n " && exit 1

# Check OS and set release variable
if [[ -f /etc/os-release ]]; then
    source /etc/os-release
    release=$ID
    elif [[ -f /usr/lib/os-release ]]; then
    source /usr/lib/os-release
    release=$ID
else
    echo "Failed to check the system OS, please contact the author!" >&2
    exit 1
fi
echo "The OS release is: $release"

arch() {
    case "$(uname -m)" in
        x86_64 | x64 | amd64) echo 'amd64' ;;
        i*86 | x86) echo '386' ;;
        armv8* | armv8 | arm64 | aarch64) echo 'arm64' ;;
        armv7* | armv7 | arm) echo 'armv7' ;;
        armv6* | armv6) echo 'armv6' ;;
        armv5* | armv5) echo 'armv5' ;;
        s390x) echo 's390x' ;;
        *) echo -e "${green}Unsupported CPU architecture! ${plain}" && rm -f install.sh && exit 1 ;;
    esac
}

echo "Arch: $(arch)"

# Simple helpers
is_ipv4() {
    [[ "$1" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && return 0 || return 1
}
is_ipv6() {
    [[ "$1" =~ : ]] && return 0 || return 1
}
is_ip() {
    is_ipv4 "$1" || is_ipv6 "$1"
}
is_domain() {
    [[ "$1" =~ ^([A-Za-z0-9](-*[A-Za-z0-9])*\.)+(xn--[a-z0-9]{2,}|[A-Za-z]{2,})$ ]] && return 0 || return 1
}

# Port helpers
is_port_in_use() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -H -ltn 2>/dev/null | grep -Eq "(^|[[:space:]])[^[:space:]]*:${port}([[:space:]]|$)" && return 0
        ss -H -lun 2>/dev/null | grep -Eq "(^|[[:space:]])[^[:space:]]*:${port}([[:space:]]|$)" && return 0
        return 1
    fi
    if command -v netstat >/dev/null 2>&1; then
        netstat -lnt 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {found=1} END {exit !found}' && return 0
        netstat -lnu 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {found=1} END {exit !found}' && return 0
        return 1
    fi
    if command -v lsof >/dev/null 2>&1; then
        lsof -nP -iTCP:${port} -sTCP:LISTEN >/dev/null 2>&1 && return 0
        lsof -nP -iUDP:${port} >/dev/null 2>&1 && return 0
    fi
    return 1
}

random_port_candidate() {
    local min_port="${1:-10000}"
    local max_port="${2:-65535}"
    local span=$((max_port - min_port + 1))
    echo $((min_port + ((((RANDOM << 15) | RANDOM)) % span)))
}

# Returns 0 if the URL host responds within a short timeout, non-zero on
# connection / DNS / TLS failure. Uses HEAD so we don't pull the full
# asset just to test reachability — important for the multi-MB xray-core
# zip and geo data files. We deliberately do NOT pass -f: a 404 still
# means the network path works, and the actual download (or apt-get
# update) will surface the real error if a path is wrong.
url_reachable() {
    curl --connect-timeout 5 --max-time 10 -sSIL -o /dev/null "$1" 2>/dev/null
}

# Probes URL reachability before downloading. If unreachable, prints a
# clear error naming the broken URL, asks the user whether to continue
# without that resource (default Y = skip and proceed), and returns 1.
# Aborts the script on N. Returns 0 on success so callers can guard
# their download blocks behind a single if-statement.
check_url_or_skip() {
    local url="$1"
    local label="$2"
    if url_reachable "$url"; then
        return 0
    fi
    echo ""
    echo -e "${yellow}══════════════════════════════════════════════════════${plain}"
    echo -e "${yellow}  Failed to reach: ${url}${plain}"
    echo -e "${yellow}  Module / file:   ${label}${plain}"
    echo -e "${yellow}══════════════════════════════════════════════════════${plain}"
    read -rp "Continue without it? [Y/n]: " __skip_choice
    case "${__skip_choice,,}" in
        n|no)
            echo -e "${red}Aborted by user.${plain}"
            exit 1
            ;;
        *)
            echo -e "${yellow}Skipping ${label}.${plain}"
            return 1
            ;;
    esac
}

pick_random_port() {
    local min_port="${1:-10000}"
    local max_port="${2:-65535}"
    shift 2
    local excluded_ports=("$@")
    local attempts=0
    local candidate=""
    local excluded=""
    local skip=0

    while [[ "$attempts" -lt 256 ]]; do
        candidate=$(random_port_candidate "$min_port" "$max_port")
        skip=0
        for excluded in "${excluded_ports[@]}"; do
            if [[ -n "$excluded" && "$candidate" -eq "$excluded" ]]; then
                skip=1
                break
            fi
        done
        if [[ "$skip" -eq 0 ]] && ! is_port_in_use "$candidate"; then
            echo "$candidate"
            return 0
        fi
        attempts=$((attempts + 1))
    done

    for ((candidate=min_port; candidate<=max_port; candidate++)); do
        skip=0
        for excluded in "${excluded_ports[@]}"; do
            if [[ -n "$excluded" && "$candidate" -eq "$excluded" ]]; then
                skip=1
                break
            fi
        done
        if [[ "$skip" -eq 0 ]] && ! is_port_in_use "$candidate"; then
            echo "$candidate"
            return 0
        fi
    done

    return 1
}

install_base() {
    case "${release}" in
        ubuntu | debian | armbian)
            apt-get update && apt-get install -y -q cron curl tar tzdata socat ca-certificates
        ;;
        fedora | amzn | virtuozzo | rhel | almalinux | rocky | ol)
            dnf -y update && dnf install -y -q curl tar tzdata socat ca-certificates
        ;;
        centos)
            if [[ "${VERSION_ID}" =~ ^7 ]]; then
                yum -y update && yum install -y curl tar tzdata socat ca-certificates
            else
                dnf -y update && dnf install -y -q curl tar tzdata socat ca-certificates
            fi
        ;;
        arch | manjaro | parch)
            pacman -Syu && pacman -Syu --noconfirm curl tar tzdata socat ca-certificates
        ;;
        opensuse-tumbleweed | opensuse-leap)
            zypper refresh && zypper -q install -y curl tar timezone socat ca-certificates
        ;;
        alpine)
            apk update && apk add curl tar tzdata socat ca-certificates
        ;;
        *)
            apt-get update && apt-get install -y -q curl tar tzdata socat ca-certificates
        ;;
    esac
}

gen_random_string() {
    local length="$1"
    local random_string=$(LC_ALL=C tr -dc 'a-zA-Z0-9' </dev/urandom | fold -w "$length" | head -n 1)
    echo "$random_string"
}

install_acme() {
    echo -e "${green}Installing acme.sh for SSL certificate management...${plain}"
    (cd ~ && curl -s https://get.acme.sh | sh >/dev/null 2>&1)
    if [ $? -ne 0 ]; then
        echo -e "${red}Failed to install acme.sh${plain}"
        return 1
    else
        echo -e "${green}acme.sh installed successfully${plain}"
    fi
    return 0
}

setup_ssl_certificate() {
    local domain="$1"
    local server_ip="$2"
    local existing_port="$3"
    local existing_webBasePath="$4"

    echo -e "${green}Setting up SSL certificate...${plain}"

    # Check if acme.sh is installed
    if ! command -v ~/.acme.sh/acme.sh &>/dev/null; then
        install_acme
        if [ $? -ne 0 ]; then
            echo -e "${yellow}Failed to install acme.sh, skipping SSL setup${plain}"
            return 1
        fi
    fi

    # Create certificate directory
    local certPath="/root/cert/${domain}"
    mkdir -p "$certPath"

    # Issue certificate
    echo -e "${green}Issuing SSL certificate for ${domain}...${plain}"
    echo -e "${yellow}Note: Port 80 must be open and accessible from the internet${plain}"

    ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt --force >/dev/null 2>&1
    ~/.acme.sh/acme.sh --issue -d ${domain} --listen-v6 --standalone --httpport 80 --force

    if [ $? -ne 0 ]; then
        echo -e "${yellow}Failed to issue certificate for ${domain}${plain}"
        echo -e "${yellow}Please ensure port 80 is open and try again later with: x-ui${plain}"
        rm -rf ~/.acme.sh/${domain} 2>/dev/null
        rm -rf "$certPath" 2>/dev/null
        return 1
    fi

    # Install certificate (|| true in reloadcmd: service may not exist yet during first install)
    ~/.acme.sh/acme.sh --installcert -d ${domain} \
        --key-file /root/cert/${domain}/privkey.pem \
        --fullchain-file /root/cert/${domain}/fullchain.pem \
        --reloadcmd "systemctl restart x-ui 2>/dev/null || true" >/dev/null 2>&1

    # Enable auto-renew
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade >/dev/null 2>&1
    # Secure permissions: private key readable only by owner
    chmod 600 $certPath/privkey.pem 2>/dev/null
    chmod 644 $certPath/fullchain.pem 2>/dev/null

    # Set certificate for panel
    local webCertFile="/root/cert/${domain}/fullchain.pem"
    local webKeyFile="/root/cert/${domain}/privkey.pem"

    if [[ -f "$webCertFile" && -f "$webKeyFile" ]]; then
        ${xui_folder}/x-ui cert -webCert "$webCertFile" -webCertKey "$webKeyFile" >/dev/null 2>&1
        echo -e "${green}SSL certificate installed and configured successfully!${plain}"
        return 0
    else
        echo -e "${yellow}Certificate files not found${plain}"
        return 1
    fi
}

# Issue Let's Encrypt IP certificate with shortlived profile (~6 days validity)
# Requires acme.sh and port 80 open for HTTP-01 challenge
setup_ip_certificate() {
    local ipv4="$1"
    local ipv6="$2"  # optional

    echo -e "${green}Setting up Let's Encrypt IP certificate (shortlived profile)...${plain}"
    echo -e "${yellow}Note: IP certificates are valid for ~6 days and will auto-renew.${plain}"
    echo -e "${yellow}Default listener is port 80. If you choose another port, ensure external port 80 forwards to it.${plain}"

    # Check for acme.sh
    if ! command -v ~/.acme.sh/acme.sh &>/dev/null; then
        install_acme
        if [ $? -ne 0 ]; then
            echo -e "${red}Failed to install acme.sh${plain}"
            return 1
        fi
    fi

    # Validate IP address
    if [[ -z "$ipv4" ]]; then
        echo -e "${red}IPv4 address is required${plain}"
        return 1
    fi

    if ! is_ipv4 "$ipv4"; then
        echo -e "${red}Invalid IPv4 address: $ipv4${plain}"
        return 1
    fi

    # Create certificate directory
    local certDir="/root/cert/ip"
    mkdir -p "$certDir"

    # Build domain arguments
    local domain_args="-d ${ipv4}"
    if [[ -n "$ipv6" ]] && is_ipv6 "$ipv6"; then
        domain_args="${domain_args} -d ${ipv6}"
        echo -e "${green}Including IPv6 address: ${ipv6}${plain}"
    fi

    # Set reload command for auto-renewal (add || true so it doesn't fail during first install)
    local reloadCmd="systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true"

    # Choose port for HTTP-01 listener (default 80, prompt override)
    local WebPort=""
    read -rp "Port to use for ACME HTTP-01 listener (default 80): " WebPort
    WebPort="${WebPort:-80}"
    if ! [[ "${WebPort}" =~ ^[0-9]+$ ]] || ((WebPort < 1 || WebPort > 65535)); then
        echo -e "${red}Invalid port provided. Falling back to 80.${plain}"
        WebPort=80
    fi
    echo -e "${green}Using port ${WebPort} for standalone validation.${plain}"
    if [[ "${WebPort}" -ne 80 ]]; then
        echo -e "${yellow}Reminder: Let's Encrypt still connects on port 80; forward external port 80 to ${WebPort}.${plain}"
    fi

    # Ensure chosen port is available
    while true; do
        if is_port_in_use "${WebPort}"; then
            echo -e "${yellow}Port ${WebPort} is in use.${plain}"

            local alt_port=""
            read -rp "Enter another port for acme.sh standalone listener (leave empty to abort): " alt_port
            alt_port="${alt_port// /}"
            if [[ -z "${alt_port}" ]]; then
                echo -e "${red}Port ${WebPort} is busy; cannot proceed.${plain}"
                return 1
            fi
            if ! [[ "${alt_port}" =~ ^[0-9]+$ ]] || ((alt_port < 1 || alt_port > 65535)); then
                echo -e "${red}Invalid port provided.${plain}"
                return 1
            fi
            WebPort="${alt_port}"
            continue
        else
            echo -e "${green}Port ${WebPort} is free and ready for standalone validation.${plain}"
            break
        fi
    done

    # Issue certificate with shortlived profile
    echo -e "${green}Issuing IP certificate for ${ipv4}...${plain}"
    ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt --force >/dev/null 2>&1

    ~/.acme.sh/acme.sh --issue \
        ${domain_args} \
        --standalone \
        --server letsencrypt \
        --certificate-profile shortlived \
        --days 6 \
        --httpport ${WebPort} \
        --force

    if [ $? -ne 0 ]; then
        echo -e "${red}Failed to issue IP certificate${plain}"
        echo -e "${yellow}Please ensure port ${WebPort} is reachable (or forwarded from external port 80)${plain}"
        # Cleanup acme.sh data for both IPv4 and IPv6 if specified
        rm -rf ~/.acme.sh/${ipv4} 2>/dev/null
        [[ -n "$ipv6" ]] && rm -rf ~/.acme.sh/${ipv6} 2>/dev/null
        rm -rf ${certDir} 2>/dev/null
        return 1
    fi

    echo -e "${green}Certificate issued successfully, installing...${plain}"

    # Install certificate
    # Note: acme.sh may report "Reload error" and exit non-zero if reloadcmd fails,
    # but the cert files are still installed. We check for files instead of exit code.
    ~/.acme.sh/acme.sh --installcert -d ${ipv4} \
        --key-file "${certDir}/privkey.pem" \
        --fullchain-file "${certDir}/fullchain.pem" \
        --reloadcmd "${reloadCmd}" 2>&1 || true

    # Verify certificate files exist (don't rely on exit code - reloadcmd failure causes non-zero)
    if [[ ! -f "${certDir}/fullchain.pem" || ! -f "${certDir}/privkey.pem" ]]; then
        echo -e "${red}Certificate files not found after installation${plain}"
        # Cleanup acme.sh data for both IPv4 and IPv6 if specified
        rm -rf ~/.acme.sh/${ipv4} 2>/dev/null
        [[ -n "$ipv6" ]] && rm -rf ~/.acme.sh/${ipv6} 2>/dev/null
        rm -rf ${certDir} 2>/dev/null
        return 1
    fi

    echo -e "${green}Certificate files installed successfully${plain}"

    # Enable auto-upgrade for acme.sh (ensures cron job runs)
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade >/dev/null 2>&1

    # Secure permissions: private key readable only by owner
    chmod 600 ${certDir}/privkey.pem 2>/dev/null
    chmod 644 ${certDir}/fullchain.pem 2>/dev/null

    # Configure panel to use the certificate
    echo -e "${green}Setting certificate paths for the panel...${plain}"
    ${xui_folder}/x-ui cert -webCert "${certDir}/fullchain.pem" -webCertKey "${certDir}/privkey.pem"

    if [ $? -ne 0 ]; then
        echo -e "${yellow}Warning: Could not set certificate paths automatically${plain}"
        echo -e "${yellow}Certificate files are at:${plain}"
        echo -e "  Cert: ${certDir}/fullchain.pem"
        echo -e "  Key:  ${certDir}/privkey.pem"
    else
        echo -e "${green}Certificate paths configured successfully${plain}"
    fi

    echo -e "${green}IP certificate installed and configured successfully!${plain}"
    echo -e "${green}Certificate valid for ~6 days, auto-renews via acme.sh cron job.${plain}"
    echo -e "${yellow}acme.sh will automatically renew and reload x-ui before expiry.${plain}"
    return 0
}

generate_self_signed_cert() {
    local host="$1"
    local certPath="/root/cert/self-signed"
    mkdir -p "$certPath"
    local certFile="${certPath}/fullchain.cer"
    local keyFile="${certPath}/private.key"

    echo -e "${yellow}Generating self-signed SSL certificate for ${host}...${plain}"
    openssl req -x509 -newkey rsa:2048 -nodes \
        -keyout "$keyFile" \
        -out "$certFile" \
        -days 3650 \
        -subj "/CN=${host}" \
        -addext "subjectAltName=IP:${host}" \
        2>/dev/null

    if [[ $? -eq 0 ]]; then
        ${xui_folder}/x-ui cert -webCert "$certFile" -webCertKey "$keyFile" >/dev/null 2>&1
        echo -e "${green}✓ Self-signed certificate generated (valid 10 years).${plain}"
        echo -e "${yellow}  Note: Browser will show a security warning — this is expected for self-signed certs.${plain}"
        return 0
    else
        echo -e "${red}Failed to generate self-signed certificate.${plain}"
        return 1
    fi
}

# Comprehensive manual SSL certificate issuance via acme.sh
ssl_cert_issue() {
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep 'webBasePath:' | awk -F': ' '{print $2}' | tr -d '[:space:]' | sed 's#^/##')
    local existing_port=$(${xui_folder}/x-ui setting -show true | grep 'port:' | awk -F': ' '{print $2}' | tr -d '[:space:]')

    # check for acme.sh first
    if ! command -v ~/.acme.sh/acme.sh &>/dev/null; then
        echo "acme.sh could not be found. Installing now..."
        (cd ~ && curl -s https://get.acme.sh | sh)
        if [ $? -ne 0 ]; then
            echo -e "${red}Failed to install acme.sh${plain}"
            return 1
        else
            echo -e "${green}acme.sh installed successfully${plain}"
        fi
    fi

    # get the domain here, and we need to verify it
    local domain=""
    while true; do
        read -rp "Please enter your domain name: " domain
        domain="${domain// /}"  # Trim whitespace

        if [[ -z "$domain" ]]; then
            echo -e "${red}Domain name cannot be empty. Please try again.${plain}"
            continue
        fi

        if ! is_domain "$domain"; then
            echo -e "${red}Invalid domain format: ${domain}. Please enter a valid domain name.${plain}"
            continue
        fi

        break
    done
    echo -e "${green}Your domain is: ${domain}, checking it...${plain}"

    # check if there already exists a certificate
    local currentCert=$(~/.acme.sh/acme.sh --list | tail -1 | awk '{print $1}')
    if [ "${currentCert}" == "${domain}" ]; then
        local certInfo=$(~/.acme.sh/acme.sh --list)
        echo -e "${red}System already has certificates for this domain. Cannot issue again.${plain}"
        echo -e "${yellow}Current certificate details:${plain}"
        echo "$certInfo"
        return 1
    else
        echo -e "${green}Your domain is ready for issuing certificates now...${plain}"
    fi

    # create a directory for the certificate
    certPath="/root/cert/${domain}"
    if [ ! -d "$certPath" ]; then
        mkdir -p "$certPath"
    else
        rm -rf "$certPath"
        mkdir -p "$certPath"
    fi

    # get the port number for the standalone server
    local WebPort=80
    read -rp "Please choose which port to use (default is 80): " WebPort
    if [[ ${WebPort} -gt 65535 || ${WebPort} -lt 1 ]]; then
        echo -e "${yellow}Your input ${WebPort} is invalid, will use default port 80.${plain}"
        WebPort=80
    fi
    echo -e "${green}Will use port: ${WebPort} to issue certificates. Please make sure this port is open.${plain}"

    # Stop panel temporarily
    echo -e "${yellow}Stopping panel temporarily...${plain}"
    systemctl stop x-ui 2>/dev/null || rc-service x-ui stop 2>/dev/null

    # issue the certificate
    ~/.acme.sh/acme.sh --set-default-ca --server letsencrypt --force
    ~/.acme.sh/acme.sh --issue -d ${domain} --listen-v6 --standalone --httpport ${WebPort} --force
    if [ $? -ne 0 ]; then
        echo -e "${red}Issuing certificate failed, please check logs.${plain}"
        rm -rf ~/.acme.sh/${domain}
        systemctl start x-ui 2>/dev/null || rc-service x-ui start 2>/dev/null
        return 1
    else
        echo -e "${green}Issuing certificate succeeded, installing certificates...${plain}"
    fi

    # Setup reload command (|| true ensures installcert doesn't fail if x-ui service
    # doesn't exist yet during first install — it gets created later in the script)
    reloadCmd="systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null || true"
    echo -e "${green}Default --reloadcmd for ACME is: ${yellow}systemctl restart x-ui${plain}"
    echo -e "${green}This command will run on every certificate issue and renew.${plain}"
    read -rp "Would you like to modify --reloadcmd for ACME? (y/n): " setReloadcmd
    if [[ "$setReloadcmd" == "y" || "$setReloadcmd" == "Y" ]]; then
        echo -e "\n${green}\t1.${plain} Preset: systemctl reload nginx ; systemctl restart x-ui"
        echo -e "${green}\t2.${plain} Input your own command"
        echo -e "${green}\t0.${plain} Keep default reloadcmd"
        read -rp "Choose an option: " choice
        case "$choice" in
        1)
            echo -e "${green}Reloadcmd is: systemctl reload nginx ; systemctl restart x-ui${plain}"
            reloadCmd="systemctl reload nginx ; systemctl restart x-ui"
            ;;
        2)
            echo -e "${yellow}It's recommended to put x-ui restart at the end${plain}"
            read -rp "Please enter your custom reloadcmd: " reloadCmd
            echo -e "${green}Reloadcmd is: ${reloadCmd}${plain}"
            ;;
        *)
            echo -e "${green}Keeping default reloadcmd${plain}"
            ;;
        esac
    fi

    # install the certificate
    ~/.acme.sh/acme.sh --installcert -d ${domain} \
        --key-file /root/cert/${domain}/privkey.pem \
        --fullchain-file /root/cert/${domain}/fullchain.pem --reloadcmd "${reloadCmd}"

    if [ $? -ne 0 ]; then
        echo -e "${yellow}Certificate reload command had issues (service may not be running yet).${plain}"
        echo -e "${yellow}Certificate files are installed — the panel will pick them up on start.${plain}"
    else
        echo -e "${green}Installing certificate succeeded, enabling auto renew...${plain}"
    fi

    # enable auto-renew
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade
    if [ $? -ne 0 ]; then
        echo -e "${yellow}Auto renew setup had issues, certificate details:${plain}"
        ls -lah /root/cert/${domain}/
        # Secure permissions: private key readable only by owner
        chmod 600 $certPath/privkey.pem 2>/dev/null
        chmod 644 $certPath/fullchain.pem 2>/dev/null
    else
        echo -e "${green}Auto renew succeeded, certificate details:${plain}"
        ls -lah /root/cert/${domain}/
        # Secure permissions: private key readable only by owner
        chmod 600 $certPath/privkey.pem 2>/dev/null
        chmod 644 $certPath/fullchain.pem 2>/dev/null
    fi

    # start panel
    systemctl start x-ui 2>/dev/null || rc-service x-ui start 2>/dev/null

    # Prompt user to set panel paths after successful certificate installation
    read -rp "Would you like to set this certificate for the panel? (y/n): " setPanel
    if [[ "$setPanel" == "y" || "$setPanel" == "Y" ]]; then
        local webCertFile="/root/cert/${domain}/fullchain.pem"
        local webKeyFile="/root/cert/${domain}/privkey.pem"

        if [[ -f "$webCertFile" && -f "$webKeyFile" ]]; then
            ${xui_folder}/x-ui cert -webCert "$webCertFile" -webCertKey "$webKeyFile"
            echo -e "${green}Certificate paths set for the panel${plain}"
            echo -e "${green}Certificate File: $webCertFile${plain}"
            echo -e "${green}Private Key File: $webKeyFile${plain}"
            echo ""
            echo -e "${green}Access URL: https://${domain}:${existing_port}/${existing_webBasePath}${plain}"
            echo -e "${yellow}Panel will restart to apply SSL certificate...${plain}"
            systemctl restart x-ui 2>/dev/null || rc-service x-ui restart 2>/dev/null
        else
            echo -e "${red}Error: Certificate or private key file not found for domain: $domain.${plain}"
        fi
    else
        echo -e "${yellow}Skipping panel path setting.${plain}"
    fi

    return 0
}

# Reusable interactive SSL setup (domain or IP)
# Sets global `SSL_HOST` to the chosen domain/IP for Access URL usage
prompt_and_setup_ssl() {
    local panel_port="$1"
    local web_base_path="$2"   # expected without leading slash
    local server_ip="$3"

    local ssl_choice=""

    echo -e "${yellow}Choose SSL certificate setup method:${plain}"
    echo -e "${green}1.${plain} Let's Encrypt for Domain (90-day validity, auto-renews)"
    echo -e "${green}2.${plain} Let's Encrypt for IP Address (6-day validity, auto-renews)"
    echo -e "${green}3.${plain} Custom SSL Certificate (Path to existing files)"
    echo -e "${blue}Note:${plain} Options 1 & 2 require port 80 open. Option 3 requires manual paths."
    read -rp "Choose an option (default 2 for IP): " ssl_choice
    ssl_choice="${ssl_choice// /}"  # Trim whitespace

    # Default to 2 (IP cert) if input is empty or invalid (not 1 or 3)
    if [[ "$ssl_choice" != "1" && "$ssl_choice" != "3" ]]; then
        ssl_choice="2"
    fi

    case "$ssl_choice" in
    1)
        # User chose Let's Encrypt domain option
        echo -e "${green}Using Let's Encrypt for domain certificate...${plain}"
        ssl_cert_issue
        # Extract the domain that was used from the certificate
        local cert_domain=$(~/.acme.sh/acme.sh --list 2>/dev/null | tail -1 | awk '{print $1}')
        if [[ -n "${cert_domain}" ]]; then
            SSL_HOST="${cert_domain}"
            echo -e "${green}✓ SSL certificate configured successfully with domain: ${cert_domain}${plain}"
        else
            echo -e "${yellow}SSL setup may have completed, but domain extraction failed${plain}"
            SSL_HOST="${server_ip}"
        fi
        ;;
    2)
        # User chose Let's Encrypt IP certificate option
        echo -e "${green}Using Let's Encrypt for IP certificate (shortlived profile)...${plain}"

        # Auto-detect IPv6 and offer to include it
        local ipv6_addr=""
        local detected_ipv6=""
        detected_ipv6=$(ip -6 addr show scope global 2>/dev/null \
            | grep -oP 'inet6\s+\K[0-9a-f:]+(?=/\d+)' | grep -v '^fe80' | head -1)
        if [[ -n "$detected_ipv6" ]]; then
            echo -e "${green}IPv6 address detected: ${detected_ipv6}${plain}"
            read -rp "Include it in the certificate? (Y/n, default: y): " ipv6_choice
            if [[ -z "${ipv6_choice}" || "${ipv6_choice,,}" == "y" ]]; then
                ipv6_addr="$detected_ipv6"
            fi
        fi

        # Stop panel if running (port 80 needed)
        if [[ $release == "alpine" ]]; then
            rc-service x-ui stop >/dev/null 2>&1
        else
            systemctl stop x-ui >/dev/null 2>&1
        fi

        setup_ip_certificate "${server_ip}" "${ipv6_addr}"
        if [ $? -eq 0 ]; then
            SSL_HOST="${server_ip}"
            echo -e "${green}✓ Let's Encrypt IP certificate configured successfully${plain}"
        else
            echo -e "${red}✗ IP certificate setup failed. Falling back to self-signed certificate.${plain}"
            generate_self_signed_cert "${server_ip}"
            SSL_HOST="${server_ip}"
        fi
        ;;
    3)
        # User chose Custom Paths (User Provided) option
        echo -e "${green}Using custom existing certificate...${plain}"
        local custom_cert=""
        local custom_key=""
        local custom_domain=""

        # 3.1 Request Domain to compose Panel URL later
        read -rp "Please enter domain name certificate issued for: " custom_domain
        custom_domain="${custom_domain// /}" # Убираем пробелы

        # 3.2 Loop for Certificate Path
        while true; do
            read -rp "Input certificate path (keywords: .crt / fullchain): " custom_cert
            # Strip quotes if present
            custom_cert=$(echo "$custom_cert" | tr -d '"' | tr -d "'")

            if [[ -f "$custom_cert" && -r "$custom_cert" && -s "$custom_cert" ]]; then
                break
            elif [[ ! -f "$custom_cert" ]]; then
                echo -e "${red}Error: File does not exist! Try again.${plain}"
            elif [[ ! -r "$custom_cert" ]]; then
                echo -e "${red}Error: File exists but is not readable (check permissions)!${plain}"
            else
                echo -e "${red}Error: File is empty!${plain}"
            fi
        done

        # 3.3 Loop for Private Key Path
        while true; do
            read -rp "Input private key path (keywords: .key / privatekey): " custom_key
            # Strip quotes if present
            custom_key=$(echo "$custom_key" | tr -d '"' | tr -d "'")

            if [[ -f "$custom_key" && -r "$custom_key" && -s "$custom_key" ]]; then
                break
            elif [[ ! -f "$custom_key" ]]; then
                echo -e "${red}Error: File does not exist! Try again.${plain}"
            elif [[ ! -r "$custom_key" ]]; then
                echo -e "${red}Error: File exists but is not readable (check permissions)!${plain}"
            else
                echo -e "${red}Error: File is empty!${plain}"
            fi
        done

        # 3.4 Apply Settings via x-ui binary
        ${xui_folder}/x-ui cert -webCert "$custom_cert" -webCertKey "$custom_key" >/dev/null 2>&1

        # Set SSL_HOST for composing Panel URL
        if [[ -n "$custom_domain" ]]; then
            SSL_HOST="$custom_domain"
        else
            SSL_HOST="${server_ip}"
        fi

        echo -e "${green}✓ Custom certificate paths applied.${plain}"
        echo -e "${yellow}Note: You are responsible for renewing these files externally.${plain}"

        systemctl restart x-ui >/dev/null 2>&1 || rc-service x-ui restart >/dev/null 2>&1
        ;;
    *)
        echo -e "${red}Invalid option. Skipping SSL setup.${plain}"
        SSL_HOST="${server_ip}"
        ;;
    esac
}

# Localhost-only debug install. Plain HTTP, listen=127.0.0.1, port and
# credentials decided up-front by prompt_debug_mode(), no SSL prompt,
# no public-IP detection, no IPv6.
config_debug_mode() {
    local existing_hasDefaultCredential=$(${xui_folder}/x-ui setting -show true | grep -Eo 'hasDefaultCredential: .+' | awk '{print $2}')
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep -Eo 'webBasePath: .+' | awk '{print $2}' | sed 's#^/##')

    local config_port="${XUI_DEBUG_PORT:-8080}"

    local config_username
    local config_password
    local config_webBasePath
    if [[ "$existing_hasDefaultCredential" == "true" || ${#existing_webBasePath} -lt 4 ]]; then
        config_username=$(gen_random_string 10)
        config_password=$(gen_random_string 10)
        config_webBasePath=$(gen_random_string 18)
        ${xui_folder}/x-ui setting -username "${config_username}" -password "${config_password}" -port "${config_port}" -webBasePath "${config_webBasePath}"
    else
        config_webBasePath="${existing_webBasePath}"
        ${xui_folder}/x-ui setting -port "${config_port}"
    fi

    # Bind to loopback only.
    ${xui_folder}/x-ui setting -listenIP "127.0.0.1"

    ${xui_folder}/x-ui migrate

    echo ""
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}  Panel installed in DEBUG / localhost mode  ${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"
    if [[ -n "${config_username}" ]]; then
        echo -e "${green}Username:    ${config_username}${plain}"
        echo -e "${green}Password:    ${config_password}${plain}"
    else
        echo -e "${yellow}Username/password unchanged from previous install.${plain}"
    fi
    echo -e "${green}Port:        ${config_port}${plain}"
    echo -e "${green}WebBasePath: ${config_webBasePath}${plain}"
    echo -e "${green}Listen:      127.0.0.1 (loopback only)${plain}"
    echo -e "${green}Access URL:  http://127.0.0.1:${config_port}/${config_webBasePath}${plain}"
    echo -e "${green}             http://localhost:${config_port}/${config_webBasePath}${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${yellow}⚠ Plain HTTP, no certificate, no remote access. For local diagnostics only.${plain}"
}

config_after_install() {
    # Debug / localhost-only mode short-circuits the SSL + public-IP +
    # IPv6 logic below. The panel binds to 127.0.0.1, listens on plain
    # HTTP, defaults to port 8080, and exposes no remote endpoints.
    if [[ "${XUI_DEBUG_MODE:-}" == "1" ]]; then
        config_debug_mode
        return
    fi

    local existing_hasDefaultCredential=$(${xui_folder}/x-ui setting -show true | grep -Eo 'hasDefaultCredential: .+' | awk '{print $2}')
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep -Eo 'webBasePath: .+' | awk '{print $2}' | sed 's#^/##')
    local existing_port=$(${xui_folder}/x-ui setting -show true | grep -Eo 'port: .+' | awk '{print $2}')
    # Properly detect empty cert by checking if cert: line exists and has content after it
    local existing_cert=$(${xui_folder}/x-ui setting -getCert true | grep 'cert:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
    local URL_lists=(
        "https://api4.ipify.org"
        "https://ipv4.icanhazip.com"
        "https://v4.api.ipinfo.io/ip"
        "https://ipv4.myexternalip.com/raw"
        "https://4.ident.me"
        "https://check-host.net/ip"
    )
    local server_ip=""
    for ip_address in "${URL_lists[@]}"; do
        local response=$(curl -s -w "\n%{http_code}" --max-time 3 "${ip_address}" 2>/dev/null)
        local http_code=$(echo "$response" | tail -n1)
        local ip_result=$(echo "$response" | head -n-1 | tr -d '[:space:]')
        if [[ "${http_code}" == "200" && -n "${ip_result}" ]]; then
            server_ip="${ip_result}"
            break
        fi
    done

    # Detect public IPv6 address
    local server_ipv6=""
    server_ipv6=$(ip -6 addr show scope global 2>/dev/null \
        | grep -oP 'inet6\s+\K[0-9a-f:]+(?=/\d+)' | grep -v '^fe80' | head -1)

    if [[ ${#existing_webBasePath} -lt 4 ]]; then
        if [[ "$existing_hasDefaultCredential" == "true" ]]; then
            local config_webBasePath=$(gen_random_string 18)
            local config_username=$(gen_random_string 10)
            local config_password=$(gen_random_string 10)

            read -rp "Would you like to customize the Panel Port settings? (If not, a random port will be applied) [y/n]: " config_confirm
            if [[ "${config_confirm}" == "y" || "${config_confirm}" == "Y" ]]; then
                read -rp "Please set up the panel port: " config_port
                echo -e "${yellow}Your Panel Port is: ${config_port}${plain}"
            else
                local config_port=$(shuf -i 1024-62000 -n 1)
                echo -e "${yellow}Generated random port: ${config_port}${plain}"
            fi

            ${xui_folder}/x-ui setting -username "${config_username}" -password "${config_password}" -port "${config_port}" -webBasePath "${config_webBasePath}"

            echo ""
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${green}     SSL Certificate Setup (MANDATORY)     ${plain}"
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${yellow}For security, SSL certificate is required for all panels.${plain}"
            echo -e "${yellow}Let's Encrypt now supports both domains and IP addresses!${plain}"
            echo ""

            prompt_and_setup_ssl "${config_port}" "${config_webBasePath}" "${server_ip}"

            # Display final credentials and access information
            echo ""
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${green}     Panel Installation Complete!         ${plain}"
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${green}Username:    ${config_username}${plain}"
            echo -e "${green}Password:    ${config_password}${plain}"
            echo -e "${green}Port:        ${config_port}${plain}"
            echo -e "${green}WebBasePath: ${config_webBasePath}${plain}"
            if [[ -n "$server_ipv6" ]]; then
                echo -e "${green}Access URL IPv4: https://${SSL_HOST}:${config_port}/${config_webBasePath}${plain}"
                echo -e "${green}Access URL IPv6: https://[${server_ipv6}]:${config_port}/${config_webBasePath}${plain}"
            else
                echo -e "${green}Access URL:  https://${SSL_HOST}:${config_port}/${config_webBasePath}${plain}"
            fi
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${yellow}⚠ IMPORTANT: Save these credentials securely!${plain}"
            echo -e "${yellow}⚠ SSL Certificate: Enabled and configured${plain}"
        else
            local config_webBasePath=$(gen_random_string 18)
            echo -e "${yellow}WebBasePath is missing or too short. Generating a new one...${plain}"
            ${xui_folder}/x-ui setting -webBasePath "${config_webBasePath}"
            echo -e "${green}New WebBasePath: ${config_webBasePath}${plain}"

            # If the panel is already installed but no certificate is configured, prompt for SSL now
            if [[ -z "${existing_cert}" ]]; then
                echo ""
                echo -e "${green}═══════════════════════════════════════════${plain}"
                echo -e "${green}     SSL Certificate Setup (RECOMMENDED)   ${plain}"
                echo -e "${green}═══════════════════════════════════════════${plain}"
                echo -e "${yellow}Let's Encrypt now supports both domains and IP addresses!${plain}"
                echo ""
                prompt_and_setup_ssl "${existing_port}" "${config_webBasePath}" "${server_ip}"
                echo -e "${green}Access URL:  https://${SSL_HOST}:${existing_port}/${config_webBasePath}${plain}"
            else
                # If a cert already exists, just show the access URL
                echo -e "${green}Access URL: https://${server_ip}:${existing_port}/${config_webBasePath}${plain}"
            fi
        fi
    else
        if [[ "$existing_hasDefaultCredential" == "true" ]]; then
            local config_username=$(gen_random_string 10)
            local config_password=$(gen_random_string 10)

            echo -e "${yellow}Default credentials detected. Security update required...${plain}"
            ${xui_folder}/x-ui setting -username "${config_username}" -password "${config_password}"
            echo -e "Generated new random login credentials:"
            echo -e "###############################################"
            echo -e "${green}Username: ${config_username}${plain}"
            echo -e "${green}Password: ${config_password}${plain}"
            echo -e "###############################################"
        else
            echo -e "${green}Username, Password, and WebBasePath are properly set.${plain}"
        fi

        # Existing install: if no cert configured, prompt user for SSL setup
        # Properly detect empty cert by checking if cert: line exists and has content after it
        existing_cert=$(${xui_folder}/x-ui setting -getCert true | grep 'cert:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
        if [[ -z "$existing_cert" ]]; then
            echo ""
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${green}     SSL Certificate Setup (RECOMMENDED)   ${plain}"
            echo -e "${green}═══════════════════════════════════════════${plain}"
            echo -e "${yellow}Let's Encrypt now supports both domains and IP addresses!${plain}"
            echo ""
            prompt_and_setup_ssl "${existing_port}" "${existing_webBasePath}" "${server_ip}"
            echo -e "${green}Access URL:  https://${SSL_HOST}:${existing_port}/${existing_webBasePath}${plain}"
        else
            echo -e "${green}SSL certificate already configured. No action needed.${plain}"
        fi
    fi

    ${xui_folder}/x-ui migrate
}

install_amneziawg() {
    echo -e "${green}Installing AmneziaWG...${plain}"

    # Install ndppd for IPv6 NDP proxy (needed for native public IPv6 to clients)
    install_ndppd() {
        case "${release}" in
            ubuntu | debian | armbian)
                apt-get install -y -q ndppd 2>/dev/null || true
            ;;
            fedora | amzn | rhel | almalinux | rocky | ol | centos)
                dnf install -y ndppd 2>/dev/null || yum install -y ndppd 2>/dev/null || true
            ;;
            arch | manjaro | parch)
                pacman -Syu --noconfirm ndppd 2>/dev/null || true
            ;;
        esac
    }

    # Enable IPv6 forwarding persistently
    enable_ipv6_forwarding() {
        if ! grep -q "net.ipv6.conf.all.forwarding" /etc/sysctl.conf 2>/dev/null; then
            echo "net.ipv6.conf.all.forwarding = 1" >> /etc/sysctl.conf
        fi
        if ! grep -q "net.ipv4.ip_forward" /etc/sysctl.conf 2>/dev/null; then
            echo "net.ipv4.ip_forward = 1" >> /etc/sysctl.conf
        fi
        sysctl -p >/dev/null 2>&1 || true
    }

    # Suppress interactive prompts (including Secure Boot MOK dialog)
    export DEBIAN_FRONTEND=noninteractive
    export DEBCONF_NONINTERACTIVE_SEEN=true

    # Try to install AmneziaWG kernel module + tools
    # Method 1: official AmneziaVPN apt repo (Debian/Ubuntu)
    if [[ "${release}" == "ubuntu" || "${release}" == "debian" || "${release}" == "armbian" ]]; then
        if ! command -v awg &>/dev/null; then
            # Pre-flight check: Launchpad PPA host is frequently blocked by
            # hosting providers (especially Russian VPS). Probe before adding
            # the PPA so the user gets a clear "skip or abort" prompt instead
            # of waiting through several apt timeouts.
            if ! check_url_or_skip "https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu/dists/focal/Release" "AmneziaWG (ppa.launchpadcontent.net)"; then
                echo -e "${yellow}AmneziaWG installation skipped — install it manually later if needed:${plain}"
                echo -e "${yellow}  https://github.com/amnezia-vpn/amneziawg-linux-kernel-module${plain}"
                install_ndppd
                return
            fi
            echo -e "${yellow}Installing amneziawg from ppa:amnezia/ppa...${plain}"
            apt-get install -y -q software-properties-common python3-launchpadlib gnupg2 linux-headers-$(uname -r) 2>/dev/null || true
            # Ensure deb-src is present (required for PPA DKMS build)
            if ! grep -q "^deb-src" /etc/apt/sources.list 2>/dev/null; then
                grep "^deb " /etc/apt/sources.list | sed 's/^deb /deb-src /' >> /etc/apt/sources.list
            fi
            if [[ "${release}" == "ubuntu" ]]; then
                add-apt-repository -y ppa:amnezia/ppa 2>/dev/null && \
                apt-get update -q && \
                apt-get install -y amneziawg && \
                echo -e "${green}AmneziaWG installed successfully via PPA.${plain}" || \
                echo -e "${red}PPA install failed. Install amneziawg manually: https://github.com/amnezia-vpn/amneziawg-linux-kernel-module${plain}"
            elif [[ "${release}" == "debian" || "${release}" == "armbian" ]]; then
                apt-key adv --keyserver keyserver.ubuntu.com --recv-keys 57290828 2>/dev/null || true
                echo "deb https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu focal main" >> /etc/apt/sources.list
                echo "deb-src https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu focal main" >> /etc/apt/sources.list
                apt-get update -q && \
                apt-get install -y amneziawg && \
                echo -e "${green}AmneziaWG installed successfully.${plain}" || \
                echo -e "${red}Install failed. Install amneziawg manually: https://github.com/amnezia-vpn/amneziawg-linux-kernel-module${plain}"
            fi
            if ! command -v awg &>/dev/null; then
                echo -e "${yellow}Warning: 'awg' binary not found after installation.${plain}"
                echo -e "${yellow}The panel will work but the tunnel will not start until you install amneziawg manually.${plain}"
                echo -e "${yellow}See: https://github.com/amnezia-vpn/amneziawg-linux-kernel-module${plain}"
            fi
            # Load kernel module immediately without requiring reboot
            modprobe amneziawg 2>/dev/null || true
        else
            echo -e "${green}AmneziaWG (awg) already installed.${plain}"
            modprobe amneziawg 2>/dev/null || true
        fi
        install_ndppd
    # Method 2: other distros — try to install wireguard as fallback
    elif [[ "${release}" == "fedora" || "${release}" == "rhel" || "${release}" == "almalinux" || "${release}" == "rocky" || "${release}" == "ol" ]]; then
        if ! command -v awg &>/dev/null; then
            echo -e "${yellow}AmneziaWG not found. Installing WireGuard as fallback...${plain}"
            dnf install -y wireguard-tools 2>/dev/null || yum install -y wireguard-tools 2>/dev/null || true
            echo -e "${yellow}Note: For full AmneziaWG support install amneziawg-tools manually.${plain}"
        fi
        install_ndppd
    elif [[ "${release}" == "arch" || "${release}" == "manjaro" || "${release}" == "parch" ]]; then
        if ! command -v awg &>/dev/null; then
            pacman -Syu --noconfirm wireguard-tools 2>/dev/null || true
            # Try AUR amneziawg-dkms if yay/paru available
            if command -v yay &>/dev/null; then
                yay -S --noconfirm amneziawg-dkms amneziawg-tools 2>/dev/null || true
            elif command -v paru &>/dev/null; then
                paru -S --noconfirm amneziawg-dkms amneziawg-tools 2>/dev/null || true
            fi
        fi
        install_ndppd
    else
        echo -e "${yellow}Unknown OS. Please install amneziawg-tools manually: https://github.com/amnezia-vpn/amneziawg-linux-kernel-module${plain}"
    fi

    # Final check
    if command -v awg &>/dev/null; then
        echo -e "${green}awg: $(awg --version 2>/dev/null || echo 'installed')${plain}"
    else
        echo -e "${yellow}Warning: 'awg' binary not found. AmneziaWG panel features will work but${plain}"
        echo -e "${yellow}the tunnel will not start until you install amneziawg-tools manually.${plain}"
        echo -e "${yellow}See: https://github.com/amnezia-vpn/amneziawg-linux-kernel-module${plain}"
    fi

    enable_ipv6_forwarding
}

config_awg_defaults() {
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}     AmneziaWG Auto-Configuration          ${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"

    local db_path="/etc/x-ui/x-ui.db"
    if [[ ! -f "$db_path" ]]; then
        echo -e "${yellow}Database not found yet, skipping AWG auto-config.${plain}"
        return
    fi

    # Check if sqlite3 is available, install if not
    if ! command -v sqlite3 &>/dev/null; then
        case "${release}" in
            ubuntu | debian | armbian) apt-get install -y -q sqlite3 2>/dev/null ;;
            fedora | amzn | rhel | almalinux | rocky | ol | centos) dnf install -y sqlite 2>/dev/null || yum install -y sqlite 2>/dev/null ;;
            arch | manjaro | parch) pacman -Syu --noconfirm sqlite 2>/dev/null ;;
            alpine) apk add sqlite 2>/dev/null ;;
            *) apt-get install -y -q sqlite3 2>/dev/null ;;
        esac
    fi

    if ! command -v sqlite3 &>/dev/null; then
        echo -e "${yellow}sqlite3 not available, skipping AWG auto-config.${plain}"
        return
    fi

    # Check if awg_servers table exists (may not exist on original 3AX-UI)
    local table_exists=$(sqlite3 "$db_path" "SELECT name FROM sqlite_master WHERE type='table' AND name='awg_servers';" 2>/dev/null)
    if [[ -z "$table_exists" ]]; then
        echo -e "${yellow}AWG tables not found (new migration). They will be created on panel start.${plain}"
        echo -e "${yellow}Skipping AWG auto-config — open the AmneziaWG page in the panel to configure.${plain}"
        return
    fi

    # Skip if AWG server already configured (update scenario — do not overwrite)
    local existing=$(sqlite3 "$db_path" "SELECT COUNT(*) FROM awg_servers;" 2>/dev/null)
    if [[ "$existing" -gt 0 ]]; then
        echo -e "${green}AmneziaWG already configured, skipping.${plain}"
        return
    fi

    # --- Detect server public IPv4 ---
    local server_ipv4=""
    local ipv4_urls=("https://api4.ipify.org" "https://ipv4.icanhazip.com" "https://4.ident.me")
    for url in "${ipv4_urls[@]}"; do
        server_ipv4=$(curl -4 -s --max-time 3 "$url" 2>/dev/null | tr -d '[:space:]')
        if [[ -n "$server_ipv4" ]]; then break; fi
    done
    echo -e "  Detected server IPv4: ${green}${server_ipv4:-not found}${plain}"

    # --- Detect default network interfaces ---
    # IPv4 and IPv6 may be on different interfaces (e.g. eth0=IPv4, eth1=IPv6)
    local ext_iface=""
    local ext_iface_ipv4=""
    local ext_iface_ipv6=""

    ext_iface_ipv4=$(ip route show default 2>/dev/null | grep -oP 'dev \K\S+' | head -1)
    ext_iface_ipv6=$(ip -6 route show default 2>/dev/null | grep -oP 'dev \K\S+' | head -1)

    # If no IPv6 default route, scan all interfaces for global IPv6
    if [[ -z "$ext_iface_ipv6" ]]; then
        ext_iface_ipv6=$(ip -o -6 addr show scope global 2>/dev/null \
            | awk '{print $2}' | grep -v '^lo$' | head -1)
    fi

    # Primary external interface: prefer the one with IPv6
    ext_iface="${ext_iface_ipv6:-${ext_iface_ipv4:-eth0}}"

    echo -e "  IPv4 interface:       ${green}${ext_iface_ipv4:-none}${plain}"
    echo -e "  IPv6 interface:       ${green}${ext_iface_ipv6:-none}${plain}"
    echo -e "  External interface:   ${green}${ext_iface}${plain}"

    # --- Detect IPv6 ---
    local server_ipv6=""
    local ipv6_prefix=""
    local ipv6_addr_on_iface=""
    local ipv6_enabled=0

    # Get the global (non-link-local) IPv6 address — try IPv6 interface first, then all
    local ipv6_search_iface="${ext_iface_ipv6:-$ext_iface}"
    ipv6_addr_on_iface=$(ip -6 addr show dev "$ipv6_search_iface" scope global 2>/dev/null \
        | grep -oP 'inet6\s+\K[0-9a-f:]+/\d+' | head -1)
    # If not found on specific iface, try any interface
    if [[ -z "$ipv6_addr_on_iface" ]]; then
        ipv6_addr_on_iface=$(ip -6 addr show scope global 2>/dev/null \
            | grep -oP 'inet6\s+\K[0-9a-f:]+/\d+' | head -1)
        # Update ext_iface_ipv6 to the interface where we found it
        if [[ -n "$ipv6_addr_on_iface" ]]; then
            local ipv6_bare="${ipv6_addr_on_iface%%/*}"
            ext_iface_ipv6=$(ip -o -6 addr show scope global 2>/dev/null \
                | grep "$ipv6_bare" | awk '{print $2}' | head -1)
            ext_iface="${ext_iface_ipv6:-$ext_iface}"
        fi
    fi

    if [[ -n "$ipv6_addr_on_iface" ]]; then
        ipv6_enabled=1
        server_ipv6="$ipv6_addr_on_iface"

        # Extract base address and prefix length
        local ipv6_base="${ipv6_addr_on_iface%%/*}"
        local ipv6_mask="${ipv6_addr_on_iface##*/}"

        echo -e "  Detected server IPv6: ${green}${server_ipv6}${plain}"

        # Determine AWG IPv6 pool
        # If server has /64 or larger, we allocate a /112 from it for AWG clients
        # If server has /112 or smaller, we use the whole subnet
        if [[ "$ipv6_mask" -le 64 ]]; then
            # Use a /112 within the /64 for AWG
            # Take the /64 prefix and append ::a00:0/112 to avoid conflicts with the main server address
            local prefix64=$(echo "$ipv6_base" | sed -E 's/:[0-9a-f]*:[0-9a-f]*:[0-9a-f]*:[0-9a-f]*$//; s/::.*/::/;')
            # Normalize: use sipcalc or manual approach
            # Simpler: take first 4 groups of the IPv6 address for the /64 prefix
            prefix64=$(python3 -c "
import ipaddress
addr = ipaddress.ip_address('${ipv6_base}')
net = ipaddress.ip_network(str(addr) + '/${ipv6_mask}', strict=False)
# Get the network address of the /64
net64 = ipaddress.ip_network(str(net.network_address) + '/64', strict=False)
print(str(net64.network_address))
" 2>/dev/null)
            if [[ -n "$prefix64" ]]; then
                ipv6_prefix="${prefix64%::}:a00::/112"
                local awg_server_ipv6="${prefix64%::}:a00::1/112"
            else
                # Fallback: disable IPv6 auto-config
                ipv6_enabled=0
                echo -e "  ${yellow}Could not parse IPv6 prefix, disabling IPv6 auto-config.${plain}"
            fi
        else
            # Subnet is /112 or smaller — use it directly
            ipv6_prefix=$(python3 -c "
import ipaddress
net = ipaddress.ip_network('${ipv6_addr_on_iface}', strict=False)
print(str(net))
" 2>/dev/null)
            local awg_server_ipv6=$(python3 -c "
import ipaddress
net = ipaddress.ip_network('${ipv6_addr_on_iface}', strict=False)
first = net.network_address + 1
print(str(first) + '/' + str(net.prefixlen))
" 2>/dev/null)
        fi

        if [[ "$ipv6_enabled" -eq 1 ]]; then
            echo -e "  AWG IPv6 pool:        ${green}${ipv6_prefix}${plain}"
            echo -e "  AWG IPv6 server addr: ${green}${awg_server_ipv6}${plain}"
        fi
    else
        echo -e "  IPv6: ${yellow}not detected on any interface${plain}"
    fi

    # --- Detect IPv6 gateway ---
    local ipv6_gateway=""
    if [[ "$ipv6_enabled" -eq 1 ]]; then
        ipv6_gateway=$(ip -6 route show default 2>/dev/null | grep -oP 'via \K\S+' | head -1)
        if [[ -n "$ipv6_gateway" ]]; then
            echo -e "  IPv6 gateway:         ${green}${ipv6_gateway}${plain}"
        fi
    fi

    # --- Generate WireGuard keys for server ---
    local server_privkey=""
    local server_pubkey=""
    if command -v awg &>/dev/null; then
        server_privkey=$(awg genkey 2>/dev/null)
        server_pubkey=$(echo "$server_privkey" | awg pubkey 2>/dev/null)
    elif command -v wg &>/dev/null; then
        server_privkey=$(wg genkey 2>/dev/null)
        server_pubkey=$(echo "$server_privkey" | wg pubkey 2>/dev/null)
    fi

    if [[ -z "$server_privkey" ]]; then
        echo -e "  ${yellow}Cannot generate keys (awg/wg not found). Keys will be generated by the panel on first access.${plain}"
    else
        echo -e "  Server keys:          ${green}generated${plain}"
    fi

    # --- Find random free port for AWG ---
    local awg_port
    awg_port=$(pick_random_port 10000 65535)
    if [[ -z "$awg_port" ]]; then
        echo -e "  ${red}Failed to select a free AWG listen port.${plain}"
        return 1
    fi
    echo -e "  AWG listen port:      ${green}${awg_port}${plain}"

    # --- Endpoint ---
    local endpoint="${server_ipv4}"
    if [[ -z "$endpoint" ]]; then
        endpoint=$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo "")
    fi

    local ipv4_iface="${ext_iface_ipv4:-$ext_iface}"
    local ipv6_iface="${ext_iface_ipv6:-$ext_iface}"

    # PostUp/PostDown are left empty in the DB — the Go code auto-generates
    # them dynamically (including NDP proxy entries per client) each time
    # the server config is applied. This ensures rules are always correct
    # and include NDP proxy for every active client.

    # --- Write defaults to DB ---
    echo -e ""
    echo -e "${green}Writing AmneziaWG defaults to database...${plain}"

    local now_ms=$(date +%s%3N 2>/dev/null || echo "$(date +%s)000")

    # Delete any default row the panel may have auto-created at startup,
    # then insert the properly configured one.
    sqlite3 "$db_path" "DELETE FROM awg_servers;" 2>/dev/null
    sqlite3 "$db_path" "INSERT INTO awg_servers (
        enable, interface_name, listen_port, mtu,
        private_key, public_key,
        ipv4_address, ipv4_pool,
        ipv6_enabled, ipv6_address, ipv6_pool, ipv6_gateway,
        jc, jmin, jmax, s1, s2, h1, h2, h3, h4,
        dns, external_interface, ipv6_external_interface,
        post_up, post_down, endpoint,
        created_at, updated_at
    ) VALUES (
        0, 'awg0', ${awg_port}, 1420,
        '${server_privkey}', '${server_pubkey}',
        '10.66.66.1/24', '10.66.66.0/24',
        ${ipv6_enabled}, '${awg_server_ipv6:-}', '${ipv6_prefix:-}', '${ipv6_gateway:-}',
        4, 50, 1000, 0, 0, 1, 2, 3, 4,
        '1.1.1.1,2606:4700:4700::1111', '${ipv4_iface}', '${ipv6_iface}',
        '', '', '${endpoint}',
        ${now_ms}, ${now_ms}
    );" 2>/dev/null

    if [[ $? -eq 0 ]]; then
        echo -e "${green}AmneziaWG configured successfully!${plain}"
        echo -e ""
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "  Interface:    awg0"
        echo -e "  Listen port:  ${awg_port}"
        echo -e "  Endpoint:     ${endpoint}"
        echo -e "  IPv4 pool:    10.66.66.0/24"
        if [[ "$ipv6_enabled" -eq 1 ]]; then
            echo -e "  IPv6 pool:    ${ipv6_prefix}"
            echo -e "  IPv6 mode:    ${green}Native public addresses (NDP proxy)${plain}"
        else
            echo -e "  IPv6:         ${yellow}disabled (no IPv6 detected)${plain}"
        fi
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e ""
        echo -e "  Open the panel → ${blue}AmneziaWG${plain} page to enable and manage clients."
        echo -e ""
    else
        echo -e "${yellow}Failed to write AWG defaults (table may not exist yet).${plain}"
        echo -e "${yellow}AWG will be configured on first panel access.${plain}"
    fi
}

install_wireguard_native() {
    echo -e "${green}Installing WireGuard Native (wireguard-tools)...${plain}"

    if command -v wg &>/dev/null; then
        echo -e "${green}WireGuard (wg) already installed.${plain}"
        modprobe wireguard 2>/dev/null || true
        return
    fi

    case "${release}" in
        ubuntu | debian | armbian)
            apt-get install -y -q wireguard-tools 2>/dev/null || true
            ;;
        fedora | amzn | rhel | almalinux | rocky | ol | centos)
            dnf install -y wireguard-tools 2>/dev/null || yum install -y wireguard-tools 2>/dev/null || true
            ;;
        arch | manjaro | parch)
            pacman -Syu --noconfirm wireguard-tools 2>/dev/null || true
            ;;
        alpine)
            apk add wireguard-tools 2>/dev/null || true
            ;;
        *)
            echo -e "${yellow}Unknown OS. Please install wireguard-tools manually.${plain}"
            ;;
    esac

    modprobe wireguard 2>/dev/null || true

    if command -v wg &>/dev/null; then
        echo -e "${green}wg: $(wg --version 2>/dev/null || echo 'installed')${plain}"
    else
        echo -e "${yellow}Warning: 'wg' binary not found after installation.${plain}"
        echo -e "${yellow}The panel will work but the WireGuard Native tunnel will not start.${plain}"
        echo -e "${yellow}Install wireguard-tools manually for your distribution.${plain}"
    fi
}

config_wg_defaults() {
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}     WireGuard Native Auto-Configuration   ${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"

    local db_path="/etc/x-ui/x-ui.db"
    if [[ ! -f "$db_path" ]]; then
        echo -e "${yellow}Database not found yet, skipping WG auto-config.${plain}"
        return
    fi

    if ! command -v sqlite3 &>/dev/null; then
        echo -e "${yellow}sqlite3 not available, skipping WG auto-config.${plain}"
        return
    fi

    local table_exists=$(sqlite3 "$db_path" "SELECT name FROM sqlite_master WHERE type='table' AND name='wg_servers';" 2>/dev/null)
    if [[ -z "$table_exists" ]]; then
        echo -e "${yellow}WG tables not found (new migration). They will be created on panel start.${plain}"
        echo -e "${yellow}Skipping WG auto-config — open the WG Settings page in the panel to configure.${plain}"
        return
    fi

    local existing=$(sqlite3 "$db_path" "SELECT COUNT(*) FROM wg_servers;" 2>/dev/null)
    if [[ "$existing" -gt 0 ]]; then
        echo -e "${green}WireGuard Native already configured, skipping.${plain}"
        return
    fi

    # --- Detect server public IPv4 ---
    local server_ipv4=""
    local ipv4_urls=("https://api4.ipify.org" "https://ipv4.icanhazip.com" "https://4.ident.me")
    for url in "${ipv4_urls[@]}"; do
        server_ipv4=$(curl -4 -s --max-time 3 "$url" 2>/dev/null | tr -d '[:space:]')
        if [[ -n "$server_ipv4" ]]; then break; fi
    done

    # --- Detect network interfaces ---
    local ext_iface_ipv4=""
    local ext_iface_ipv6=""
    ext_iface_ipv4=$(ip route show default 2>/dev/null | grep -oP 'dev \K\S+' | head -1)
    ext_iface_ipv6=$(ip -6 route show default 2>/dev/null | grep -oP 'dev \K\S+' | head -1)
    if [[ -z "$ext_iface_ipv6" ]]; then
        ext_iface_ipv6=$(ip -o -6 addr show scope global 2>/dev/null | awk '{print $2}' | grep -v '^lo$' | head -1)
    fi
    local ext_iface="${ext_iface_ipv6:-${ext_iface_ipv4:-eth0}}"

    # --- Detect IPv6 ---
    local ipv6_enabled=0
    local server_ipv6=""
    local ipv6_prefix=""
    local wg_server_ipv6=""
    local ipv6_gateway=""
    local ipv6_search_iface="${ext_iface_ipv6:-$ext_iface}"
    local ipv6_addr_on_iface=""
    ipv6_addr_on_iface=$(ip -6 addr show dev "$ipv6_search_iface" scope global 2>/dev/null \
        | grep -oP 'inet6\s+\K[0-9a-f:]+/\d+' | head -1)
    if [[ -z "$ipv6_addr_on_iface" ]]; then
        ipv6_addr_on_iface=$(ip -6 addr show scope global 2>/dev/null \
            | grep -oP 'inet6\s+\K[0-9a-f:]+/\d+' | head -1)
    fi

    if [[ -n "$ipv6_addr_on_iface" ]]; then
        ipv6_enabled=1
        server_ipv6="$ipv6_addr_on_iface"
        local ipv6_base="${ipv6_addr_on_iface%%/*}"
        local ipv6_mask="${ipv6_addr_on_iface##*/}"
        if [[ "$ipv6_mask" -le 64 ]]; then
            local prefix64=$(python3 -c "
import ipaddress
addr = ipaddress.ip_address('${ipv6_base}')
net = ipaddress.ip_network(str(addr) + '/${ipv6_mask}', strict=False)
net64 = ipaddress.ip_network(str(net.network_address) + '/64', strict=False)
print(str(net64.network_address))
" 2>/dev/null)
            if [[ -n "$prefix64" ]]; then
                ipv6_prefix="${prefix64%::}:b00::/112"
                wg_server_ipv6="${prefix64%::}:b00::1/112"
            else
                ipv6_enabled=0
            fi
        else
            ipv6_prefix=$(python3 -c "
import ipaddress
net = ipaddress.ip_network('${ipv6_addr_on_iface}', strict=False)
print(str(net))
" 2>/dev/null)
            wg_server_ipv6=$(python3 -c "
import ipaddress
net = ipaddress.ip_network('${ipv6_addr_on_iface}', strict=False)
first = net.network_address + 1
print(str(first) + '/' + str(net.prefixlen))
" 2>/dev/null)
        fi
        if [[ "$ipv6_enabled" -eq 1 ]]; then
            ipv6_gateway=$(ip -6 route show default 2>/dev/null | grep -oP 'via \K\S+' | head -1)
        fi
    fi

    # --- Generate WireGuard keys ---
    local server_privkey=""
    local server_pubkey=""
    if command -v wg &>/dev/null; then
        server_privkey=$(wg genkey 2>/dev/null)
        server_pubkey=$(echo "$server_privkey" | wg pubkey 2>/dev/null)
    elif command -v awg &>/dev/null; then
        server_privkey=$(awg genkey 2>/dev/null)
        server_pubkey=$(echo "$server_privkey" | awg pubkey 2>/dev/null)
    fi

    if [[ -z "$server_privkey" ]]; then
        echo -e "  ${yellow}Cannot generate keys. Keys will be generated by the panel on first access.${plain}"
    else
        echo -e "  Server keys:          ${green}generated${plain}"
    fi

    # --- Find random free port for WG ---
    local wg_port
    wg_port=$(pick_random_port 10000 65535 "${awg_port}")
    if [[ -z "$wg_port" ]]; then
        echo -e "  ${red}Failed to select a free WG listen port.${plain}"
        return 1
    fi
    echo -e "  WG listen port:       ${green}${wg_port}${plain}"

    local endpoint="${server_ipv4}"
    if [[ -z "$endpoint" ]]; then
        endpoint=$(hostname -f 2>/dev/null || hostname 2>/dev/null || echo "")
    fi

    local ipv4_iface="${ext_iface_ipv4:-$ext_iface}"
    local ipv6_iface="${ext_iface_ipv6:-$ext_iface}"

    echo -e ""
    echo -e "${green}Writing WireGuard Native defaults to database...${plain}"

    local now_ms=$(date +%s%3N 2>/dev/null || echo "$(date +%s)000")

    sqlite3 "$db_path" "DELETE FROM wg_servers;" 2>/dev/null
    sqlite3 "$db_path" "INSERT INTO wg_servers (
        enable, interface_name, listen_port, mtu,
        private_key, public_key,
        ipv4_address, ipv4_pool,
        ipv6_enabled, ipv6_address, ipv6_pool, ipv6_gateway,
        dns, external_interface, ipv6_external_interface,
        post_up, post_down, endpoint,
        created_at, updated_at
    ) VALUES (
        0, 'wg0', ${wg_port}, 1420,
        '${server_privkey}', '${server_pubkey}',
        '10.77.77.1/24', '10.77.77.0/24',
        ${ipv6_enabled}, '${wg_server_ipv6:-}', '${ipv6_prefix:-}', '${ipv6_gateway:-}',
        '1.1.1.1,2606:4700:4700::1111', '${ipv4_iface}', '${ipv6_iface}',
        '', '', '${endpoint}',
        ${now_ms}, ${now_ms}
    );" 2>/dev/null

    if [[ $? -eq 0 ]]; then
        echo -e "${green}WireGuard Native configured successfully!${plain}"
        echo -e ""
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "  Interface:    wg0"
        echo -e "  Listen port:  ${wg_port}"
        echo -e "  Endpoint:     ${endpoint}"
        echo -e "  IPv4 pool:    10.77.77.0/24"
        if [[ "$ipv6_enabled" -eq 1 ]]; then
            echo -e "  IPv6 pool:    ${ipv6_prefix}"
            echo -e "  IPv6 mode:    ${green}Native public addresses (NDP proxy)${plain}"
        else
            echo -e "  IPv6:         ${yellow}disabled (no IPv6 detected)${plain}"
        fi
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e ""
        echo -e "  Open the panel → ${blue}WG Settings${plain} page to enable and manage clients."
        echo -e ""
    else
        echo -e "${yellow}Failed to write WG defaults (table may not exist yet).${plain}"
        echo -e "${yellow}WG will be configured on first panel access.${plain}"
    fi
}

# Translates the panel's arch label to xray-core's release naming so we can
# fetch the right Xray-linux-{ARCH}.zip from XTLS/Xray-core releases.
xray_release_arch() {
    case "$(arch)" in
        amd64) echo "64" ;;
        386) echo "32" ;;
        arm64) echo "arm64-v8a" ;;
        armv7) echo "arm32-v7a" ;;
        armv6) echo "arm32-v6" ;;
        armv5) echo "arm32-v5" ;;
        s390x) echo "s390x" ;;
        *) echo "" ;;
    esac
}

# Translates the panel's arch label to the filename Go uses for the bundled
# xray binary inside the panel installation (panel looks up
# bin/xray-linux-{FNAME}).
xray_panel_arch() {
    case "$(arch)" in
        amd64) echo "amd64" ;;
        386) echo "386" ;;
        arm64) echo "arm64" ;;
        armv7|armv6|armv5) echo "arm" ;;
        s390x) echo "s390x" ;;
        *) echo "" ;;
    esac
}

# Downloads xray binary + geo data files into the given target directory.
# Mirrors the logic in DockerInit.sh — same xray version (v26.3.27), same
# geo-data sources. Always called fresh; we deliberately never reuse a
# previously-downloaded copy from build/bin or target/bin so an install
# can't inherit stale geo rules or an old xray binary.
download_xray_and_geo() {
    local target_bin_dir="$1"
    local xray_arch xray_fname xray_url
    xray_arch=$(xray_release_arch)
    xray_fname=$(xray_panel_arch)
    if [[ -z "$xray_arch" || -z "$xray_fname" ]]; then
        echo -e "${red}No prebuilt xray-core for arch $(arch) — install fails.${plain}"
        return 1
    fi
    if ! command -v unzip >/dev/null 2>&1; then
        echo -e "${yellow}Installing unzip (needed to extract xray-core)...${plain}"
        case "${release}" in
            ubuntu|debian|armbian) apt-get install -y -q unzip >/dev/null 2>&1 ;;
            arch|manjaro|parch)    pacman -Sy --noconfirm unzip >/dev/null 2>&1 ;;
            alpine)                apk add unzip >/dev/null 2>&1 ;;
            opensuse-tumbleweed)   zypper install -y unzip >/dev/null 2>&1 ;;
            *)                     dnf install -y -q unzip >/dev/null 2>&1 || yum install -y unzip >/dev/null 2>&1 ;;
        esac
    fi

    mkdir -p "$target_bin_dir"
    local tmp_zip="/tmp/xray-core.$$.zip"
    xray_url="https://github.com/XTLS/Xray-core/releases/download/v26.3.27/Xray-linux-${xray_arch}.zip"

    # xray binary is mandatory — without it the panel can't run any
    # protocol. Probe the URL up-front so the user gets a clean prompt
    # instead of waiting through curl's full retry loop on a dead host.
    if ! check_url_or_skip "$xray_url" "xray-core binary"; then
        echo -e "${red}Cannot proceed without xray-core — aborting xray bundle download.${plain}"
        return 1
    fi
    echo -e "${green}Downloading xray-core ${xray_url}...${plain}"
    if ! curl -4fLRo "$tmp_zip" "$xray_url"; then
        rm -f "$tmp_zip"
        echo -e "${red}Failed to download xray-core.${plain}"
        return 1
    fi
    (cd "$target_bin_dir" && unzip -o "$tmp_zip" >/dev/null) || {
        rm -f "$tmp_zip"
        echo -e "${red}Failed to unzip xray-core.${plain}"
        return 1
    }
    rm -f "$tmp_zip"
    rm -f "$target_bin_dir/geoip.dat" "$target_bin_dir/geosite.dat"
    if [[ -f "$target_bin_dir/xray" ]]; then
        mv -f "$target_bin_dir/xray" "$target_bin_dir/xray-linux-${xray_fname}"
        chmod +x "$target_bin_dir/xray-linux-${xray_fname}"
    fi

    # Geo data files are optional — panel boots fine without them, just
    # falls back to no-routing-rules. Probe each before fetching.
    echo -e "${green}Downloading geo data...${plain}"
    local geo_url
    geo_url="https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat"
    check_url_or_skip "$geo_url" "geoip.dat (Loyalsoldier)" && \
        curl -4sfLRo "$target_bin_dir/geoip.dat" "$geo_url"
    geo_url="https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat"
    check_url_or_skip "$geo_url" "geosite.dat (Loyalsoldier)" && \
        curl -4sfLRo "$target_bin_dir/geosite.dat" "$geo_url"
    geo_url="https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat"
    check_url_or_skip "$geo_url" "geoip_IR.dat (Iran rules)" && \
        curl -4sfLRo "$target_bin_dir/geoip_IR.dat" "$geo_url"
    geo_url="https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat"
    check_url_or_skip "$geo_url" "geosite_IR.dat (Iran rules)" && \
        curl -4sfLRo "$target_bin_dir/geosite_IR.dat" "$geo_url"
    geo_url="https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat"
    check_url_or_skip "$geo_url" "geoip_RU.dat (Russia rules)" && \
        curl -4sfLRo "$target_bin_dir/geoip_RU.dat" "$geo_url"
    geo_url="https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat"
    check_url_or_skip "$geo_url" "geosite_RU.dat (Russia rules)" && \
        curl -4sfLRo "$target_bin_dir/geosite_RU.dat" "$geo_url"
    return 0
}

# Wrapper around download_xray_and_geo that, in debug mode only, looks
# for an already-present xray + geo bundle in well-known cache locations
# and reuses it instead of re-downloading. Production / non-debug
# installs always pull fresh upstream copies. Cache locations checked
# (in order):
#   1. ${xui_folder}/bin   — preserved across updates after the rm-rf fix.
#   2. ${SCRIPT_DIR}/build/bin — left over from an earlier local build.
#   3. ${SCRIPT_DIR}/target/bin — same.
fetch_xray_bundle_smart() {
    local target_bin_dir="$1"
    local panel_fname
    panel_fname=$(xray_panel_arch)

    if [[ "${XUI_DEBUG_MODE:-}" == "1" && -n "$panel_fname" ]]; then
        if [[ -f "$target_bin_dir/xray-linux-${panel_fname}" ]]; then
            echo -e "${green}xray-core + geo data already in ${target_bin_dir}, skipping download.${plain}"
            return 0
        fi
        local src_dir
        for src_dir in "$SCRIPT_DIR/build/bin" "$SCRIPT_DIR/target/bin"; do
            if [[ -f "$src_dir/xray-linux-${panel_fname}" ]]; then
                echo -e "${green}Reusing xray + geo bundle from ${src_dir}.${plain}"
                mkdir -p "$target_bin_dir"
                cp -f "$src_dir"/* "$target_bin_dir/" 2>/dev/null || true
                return 0
            fi
        done
    fi

    download_xray_and_geo "$target_bin_dir"
}

# Ensures a Go toolchain ≥ 1.21 is on PATH. With Go ≥ 1.21 the GOTOOLCHAIN=auto
# default makes `go build` self-bootstrap the exact version pinned in go.mod
# (currently 1.26.2), so we only need a recent-enough bootstrap. If the host
# has no `go` or only an old one, install Go 1.26.2 from go.dev into
# /usr/local/go and prepend it to PATH for the rest of this script.
ensure_go() {
    local need_install=1
    if command -v go >/dev/null 2>&1; then
        local v
        v=$(go env GOVERSION 2>/dev/null | sed -E 's/^go//')
        # GOVERSION may be empty on very old Go; treat as too old.
        if [[ -n "$v" ]]; then
            local min_version="1.21.0"
            if [[ "$(printf '%s\n' "$min_version" "$v" | sort -V | head -n1)" == "$min_version" ]]; then
                need_install=0
            fi
        fi
    fi

    if [[ $need_install -eq 0 ]]; then
        echo -e "${green}Existing Go toolchain detected: $(go env GOVERSION 2>/dev/null)${plain}"
        return 0
    fi

    local goarch
    case "$(arch)" in
        amd64)        goarch="amd64" ;;
        386)          goarch="386" ;;
        arm64)        goarch="arm64" ;;
        armv6|armv7)  goarch="armv6l" ;;
        s390x)        goarch="s390x" ;;
        *)
            echo -e "${red}No prebuilt Go binary for arch $(arch) — cannot bootstrap.${plain}"
            return 1
            ;;
    esac

    local go_version="1.26.2"
    local go_url="https://go.dev/dl/go${go_version}.linux-${goarch}.tar.gz"
    local tmp_tgz="/tmp/go-bootstrap.$$.tar.gz"

    if ! check_url_or_skip "$go_url" "Go ${go_version} bootstrap"; then
        return 1
    fi
    echo -e "${green}Installing Go ${go_version} from ${go_url}...${plain}"
    if ! curl -4fLRo "$tmp_tgz" "$go_url"; then
        rm -f "$tmp_tgz"
        echo -e "${red}Failed to download Go ${go_version}.${plain}"
        return 1
    fi
    rm -rf /usr/local/go
    if ! tar -C /usr/local -xzf "$tmp_tgz"; then
        rm -f "$tmp_tgz"
        echo -e "${red}Failed to extract Go ${go_version}.${plain}"
        return 1
    fi
    rm -f "$tmp_tgz"
    export PATH="/usr/local/go/bin:$PATH"
    if ! command -v go >/dev/null 2>&1; then
        echo -e "${red}Go installed but not on PATH — aborting build.${plain}"
        return 1
    fi
    echo -e "${green}Go installed: $(go version)${plain}"
    return 0
}

# Builds the panel binary from the local source tree and assembles the same
# directory layout the GitHub release tarball would extract into
# (${xui_folder}/x-ui, x-ui.sh, service-unit files, bin/xray-linux-…). After
# this returns, install_x-ui's existing post-extract logic (chmod, service
# install, etc.) takes over unchanged.
install_x-ui_from_source() {
    echo -e "${green}Local source detected at ${SCRIPT_DIR} — building from source...${plain}"
    if ! ensure_go; then
        echo -e "${yellow}Falling back to GitHub release download.${plain}"
        return 1
    fi

    local build_version
    build_version=$(cd "$SCRIPT_DIR" && git describe --tags --always --dirty 2>/dev/null)
    if [[ -z "$build_version" ]]; then
        build_version="v$(cat "$SCRIPT_DIR/config/version" 2>/dev/null || echo unknown)"
    fi
    echo -e "${green}Building x-ui (version ${build_version})...${plain}"

    (cd "$SCRIPT_DIR" && \
     GOTOOLCHAIN=auto CGO_ENABLED=1 go build \
         -ldflags "-w -s -X 'github.com/coinman-dev/3ax-ui/v2/config.version=${build_version}'" \
         -o "$SCRIPT_DIR/build/x-ui" main.go) || {
        echo -e "${red}go build failed — falling back to GitHub release.${plain}"
        return 1
    }

    # Replace only the files we own. Crucially we keep x-ui.db (panel
    # database) and bin/ (xray + geo data) intact across re-installs so a
    # repeated install doesn't wipe user data or trigger an unnecessary
    # multi-MB xray re-download.
    mkdir -p "${xui_folder}/bin"
    rm -f "${xui_folder}/x-ui" \
          "${xui_folder}/x-ui.sh" \
          "${xui_folder}/x-ui.service" \
          "${xui_folder}/x-ui.service.debian" \
          "${xui_folder}/x-ui.service.arch" \
          "${xui_folder}/x-ui.service.rhel" \
          "${xui_folder}/x-ui.rc"
    cp -f "$SCRIPT_DIR/build/x-ui"           "${xui_folder}/x-ui"
    cp -f "$SCRIPT_DIR/x-ui.sh"              "${xui_folder}/x-ui.sh"
    [[ -f "$SCRIPT_DIR/x-ui.service.debian" ]] && cp -f "$SCRIPT_DIR/x-ui.service.debian" "${xui_folder}/"
    [[ -f "$SCRIPT_DIR/x-ui.service.arch"   ]] && cp -f "$SCRIPT_DIR/x-ui.service.arch"   "${xui_folder}/"
    [[ -f "$SCRIPT_DIR/x-ui.service.rhel"   ]] && cp -f "$SCRIPT_DIR/x-ui.service.rhel"   "${xui_folder}/"
    [[ -f "$SCRIPT_DIR/x-ui.rc"             ]] && cp -f "$SCRIPT_DIR/x-ui.rc"             "${xui_folder}/"

    if ! fetch_xray_bundle_smart "${xui_folder}/bin"; then
        echo -e "${red}Failed to fetch xray-core for the local-source install.${plain}"
        return 1
    fi

    cp -f "${xui_folder}/x-ui.sh" /usr/bin/x-ui-temp
    tag_version="${build_version}"
    return 0
}

# Common post-extract steps shared by both the GitHub-release and
# local-source install paths. Assumes ${xui_folder} is fully populated and
# CWD is ${xui_folder}.
install_x-ui_finalize() {
    chmod +x x-ui
    [ -f x-ui.sh ] && chmod +x x-ui.sh

    # Rename the bundled xray binary for arm variants — the panel always loads
    # bin/xray-linux-arm regardless of the specific arm version.
    if [[ $(arch) == "armv5" || $(arch) == "armv6" || $(arch) == "armv7" ]]; then
        [ -f bin/xray-linux-$(arch) ] && mv bin/xray-linux-$(arch) bin/xray-linux-arm
        [ -f bin/xray-linux-arm ] && chmod +x bin/xray-linux-arm
    fi
    chmod +x x-ui
    [ -f bin/xray-linux-$(arch) ] && chmod +x bin/xray-linux-$(arch)

    mv -f /usr/bin/x-ui-temp /usr/bin/x-ui
    chmod +x /usr/bin/x-ui
    mkdir -p /var/log/x-ui
    config_after_install
    config_awg_defaults
    config_wg_defaults

    # Etckeeper compatibility
    if [ -d "/etc/.git" ]; then
        if [ -f "/etc/.gitignore" ]; then
            if ! grep -q "x-ui/x-ui.db" "/etc/.gitignore"; then
                echo "" >> "/etc/.gitignore"
                echo "x-ui/x-ui.db" >> "/etc/.gitignore"
                echo -e "${green}Added x-ui.db to /etc/.gitignore for etckeeper${plain}"
            fi
        else
            echo "x-ui/x-ui.db" > "/etc/.gitignore"
            echo -e "${green}Created /etc/.gitignore and added x-ui.db for etckeeper${plain}"
        fi
    fi

    install_x-ui_service_unit
    print_install_footer
}

# Installs and starts the OS service unit. Prefers the file embedded in
# ${xui_folder}/ (delivered both by the release tarball and by the local-source
# build); falls back to downloading from GitHub raw if missing.
install_x-ui_service_unit() {
    if [[ $release == "alpine" ]]; then
        if [ -f "${xui_folder}/x-ui.rc" ]; then
            cp -f "${xui_folder}/x-ui.rc" /etc/init.d/x-ui
        else
            curl -4fLRo /etc/init.d/x-ui https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.rc
            if [[ $? -ne 0 ]]; then
                echo -e "${red}Failed to download x-ui.rc${plain}"
                exit 1
            fi
        fi
        chmod +x /etc/init.d/x-ui
        rc-update add x-ui
        rc-service x-ui start
        return
    fi

    # systemd path
    local service_installed=false

    if [ -f "${xui_folder}/x-ui.service" ]; then
        echo -e "${green}Found x-ui.service in extracted files, installing...${plain}"
        cp -f "${xui_folder}/x-ui.service" ${xui_service}/ >/dev/null 2>&1 && service_installed=true
    fi

    if [ "$service_installed" = false ]; then
        case "${release}" in
            ubuntu | debian | armbian)
                if [ -f "${xui_folder}/x-ui.service.debian" ]; then
                    echo -e "${green}Found x-ui.service.debian in extracted files, installing...${plain}"
                    cp -f "${xui_folder}/x-ui.service.debian" ${xui_service}/x-ui.service >/dev/null 2>&1 && service_installed=true
                fi
            ;;
            arch | manjaro | parch)
                if [ -f "${xui_folder}/x-ui.service.arch" ]; then
                    echo -e "${green}Found x-ui.service.arch in extracted files, installing...${plain}"
                    cp -f "${xui_folder}/x-ui.service.arch" ${xui_service}/x-ui.service >/dev/null 2>&1 && service_installed=true
                fi
            ;;
            *)
                if [ -f "${xui_folder}/x-ui.service.rhel" ]; then
                    echo -e "${green}Found x-ui.service.rhel in extracted files, installing...${plain}"
                    cp -f "${xui_folder}/x-ui.service.rhel" ${xui_service}/x-ui.service >/dev/null 2>&1 && service_installed=true
                fi
            ;;
        esac
    fi

    # If service file not found locally, download from GitHub
    if [ "$service_installed" = false ]; then
        echo -e "${yellow}Service files not found locally, downloading from GitHub...${plain}"
        case "${release}" in
            ubuntu | debian | armbian)
                curl -4fLRo ${xui_service}/x-ui.service https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.service.debian >/dev/null 2>&1
            ;;
            arch | manjaro | parch)
                curl -4fLRo ${xui_service}/x-ui.service https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.service.arch >/dev/null 2>&1
            ;;
            *)
                curl -4fLRo ${xui_service}/x-ui.service https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.service.rhel >/dev/null 2>&1
            ;;
        esac
        if [[ $? -ne 0 ]]; then
            echo -e "${red}Failed to install x-ui.service from GitHub${plain}"
            exit 1
        fi
        service_installed=true
    fi

    echo -e "${green}Setting up systemd unit...${plain}"
    chown root:root ${xui_service}/x-ui.service >/dev/null 2>&1
    chmod 644 ${xui_service}/x-ui.service >/dev/null 2>&1
    systemctl daemon-reload
    systemctl enable x-ui
    systemctl start x-ui
}

print_install_footer() {
    echo -e "${green}x-ui ${tag_version}${plain} installation finished, it is running now..."
    echo -e ""
    echo -e "┌───────────────────────────────────────────────────────┐
│  ${blue}x-ui control menu usages (subcommands):${plain}              │
│                                                       │
│  ${blue}x-ui${plain}              - Admin Management Script          │
│  ${blue}x-ui start${plain}        - Start                            │
│  ${blue}x-ui stop${plain}         - Stop                             │
│  ${blue}x-ui restart${plain}      - Restart                          │
│  ${blue}x-ui status${plain}       - Current Status                   │
│  ${blue}x-ui settings${plain}     - Current Settings                 │
│  ${blue}x-ui enable${plain}       - Enable Autostart on OS Startup   │
│  ${blue}x-ui disable${plain}      - Disable Autostart on OS Startup  │
│  ${blue}x-ui log${plain}          - Check logs                       │
│  ${blue}x-ui banlog${plain}       - Check Fail2ban ban logs          │
│  ${blue}x-ui update${plain}       - Update                           │
│  ${blue}x-ui legacy${plain}       - Legacy version                   │
│  ${blue}x-ui install${plain}      - Install                          │
│  ${blue}x-ui uninstall${plain}    - Uninstall                        │
└───────────────────────────────────────────────────────┘"
}

install_x-ui() {
    cd ${xui_folder%/x-ui}/

    # Stop any running x-ui before swapping files. Both install paths need
    # this and it is safe even on a first install (commands silently noop).
    if [[ -e ${xui_folder}/ ]]; then
        if [[ $release == "alpine" ]]; then
            rc-service x-ui stop >/dev/null 2>&1
        else
            systemctl stop x-ui >/dev/null 2>&1
        fi
    fi

    if is_local_source_install; then
        if install_x-ui_from_source; then
            cd "${xui_folder}"
            install_x-ui_finalize
            return
        fi
        echo -e "${yellow}Local-source build did not complete — proceeding with GitHub release.${plain}"
    fi

    # Download resources
    if [ $# == 0 ]; then
        tag_version=$(curl -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ ! -n "$tag_version" ]]; then
            echo -e "${yellow}Trying to fetch version with IPv4...${plain}"
            tag_version=$(curl -4 -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
            if [[ ! -n "$tag_version" ]]; then
                echo -e "${red}Failed to fetch x-ui version, it may be due to GitHub API restrictions, please try it later${plain}"
                exit 1
            fi
        fi
        echo -e "Got x-ui latest stable version: ${tag_version}, beginning the installation..."
        curl -4fLRo ${xui_folder}-linux-$(arch).tar.gz https://github.com/coinman-dev/3ax-ui/releases/download/${tag_version}/x-ui-linux-$(arch).tar.gz
        if [[ $? -ne 0 ]]; then
            echo -e "${red}Downloading x-ui failed, please be sure that your server can access GitHub ${plain}"
            exit 1
        fi
    elif [[ "$1" == "--beta" || "$1" == "--pre" ]]; then
        echo -e "${yellow}Installing latest pre-release version...${plain}"
        tag_version=$(curl -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -1)
        if [[ ! -n "$tag_version" ]]; then
            echo -e "${yellow}Trying to fetch version with IPv4...${plain}"
            tag_version=$(curl -4 -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -1)
            if [[ ! -n "$tag_version" ]]; then
                echo -e "${red}Failed to fetch x-ui version, it may be due to GitHub API restrictions, please try it later${plain}"
                exit 1
            fi
        fi
        echo -e "Got x-ui latest pre-release version: ${tag_version}, beginning the installation..."
        curl -4fLRo ${xui_folder}-linux-$(arch).tar.gz https://github.com/coinman-dev/3ax-ui/releases/download/${tag_version}/x-ui-linux-$(arch).tar.gz
        if [[ $? -ne 0 ]]; then
            echo -e "${red}Downloading x-ui failed, please be sure that your server can access GitHub ${plain}"
            exit 1
        fi
    else
        tag_version=$1
        tag_version_numeric=${tag_version#v}
        min_version="2.3.5"

        if [[ "$(printf '%s\n' "$min_version" "$tag_version_numeric" | sort -V | head -n1)" != "$min_version" ]]; then
            echo -e "${red}Please use a newer version (at least v2.3.5). Exiting installation.${plain}"
            exit 1
        fi

        url="https://github.com/coinman-dev/3ax-ui/releases/download/${tag_version}/x-ui-linux-$(arch).tar.gz"
        echo -e "Beginning to install x-ui $1"
        curl -4fLRo ${xui_folder}-linux-$(arch).tar.gz ${url}
        if [[ $? -ne 0 ]]; then
            echo -e "${red}Download x-ui $1 failed, please check if the version exists ${plain}"
            exit 1
        fi
    fi
    curl -4fLRo /usr/bin/x-ui-temp https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.sh
    if [[ $? -ne 0 ]]; then
        echo -e "${red}Failed to download x-ui.sh${plain}"
        exit 1
    fi

    # Remove old install before extracting fresh tarball.
    if [[ -e ${xui_folder}/ ]]; then
        rm ${xui_folder}/ -rf
    fi

    # Extract resources and set permissions
    tar zxvf x-ui-linux-$(arch).tar.gz
    rm x-ui-linux-$(arch).tar.gz -f

    cd x-ui
    install_x-ui_finalize
}

# Diagnostic / localhost-only install. When enabled the panel binds to
# 127.0.0.1 only, runs over plain HTTP, defaults to port 8080, and skips
# the SSL / public-IP / IPv6 prompts. Protocol stacks (AmneziaWG,
# WireGuard Native, xray) are still installed normally — only the
# panel's web access is restricted. Activated either by the interactive
# prompt below or by pre-setting XUI_DEBUG_MODE=1 in the environment.
prompt_debug_mode() {
    if [[ "${XUI_DEBUG_MODE:-}" == "1" ]]; then
        echo -e "${yellow}Debug mode enabled via XUI_DEBUG_MODE=1.${plain}"
        : "${XUI_DEBUG_PORT:=8080}"
        export XUI_DEBUG_PORT
        return
    fi
    echo ""
    echo -e "${yellow}Install panel in debug / diagnostic mode (localhost only)? [y/N]${plain}"
    echo -e "${yellow}(HTTP only, listen=127.0.0.1, default port 8080, no SSL or IPv6)${plain}"
    read -rp "Debug mode? [y/N]: " __debug_choice
    case "${__debug_choice,,}" in
        y|yes)
            export XUI_DEBUG_MODE=1
            # Ask for the panel port immediately so the user sees the
            # complete debug-mode setup decided up-front, before any
            # downloads or installs run. read -rp doesn't render color
            # escapes, so we print the prompt with echo -en first.
            local __port_choice=""
            echo -en "${yellow}Panel port for debug mode? [8080]: ${plain}"
            read -r __port_choice
            if [[ -n "${__port_choice}" ]]; then
                export XUI_DEBUG_PORT="${__port_choice}"
            else
                export XUI_DEBUG_PORT=8080
            fi
            echo -e "${green}Debug mode enabled — panel will be installed on http://127.0.0.1:${XUI_DEBUG_PORT}${plain}"
            ;;
        *)
            export XUI_DEBUG_MODE=0
            ;;
    esac
}

echo -e "${green}Running...${plain}"
prompt_debug_mode
install_base
install_amneziawg
install_wireguard_native
install_x-ui $1

# Secure Boot warning
# Try mokutil first, fall back to reading EFI variable directly
check_secure_boot() {
    if command -v mokutil &>/dev/null; then
        mokutil --sb-state 2>/dev/null | grep -q "SecureBoot enabled"
        return $?
    fi
    # No mokutil: read SecureBoot EFI variable directly (byte 4 = 1 means enabled)
    local sb_var
    sb_var=$(find /sys/firmware/efi/efivars -name "SecureBoot-*" 2>/dev/null | head -1)
    if [[ -n "$sb_var" ]]; then
        [[ "$(od -An -tu1 -j4 -N1 "$sb_var" 2>/dev/null | tr -d ' ')" == "1" ]]
        return $?
    fi
    # No EFI at all — Secure Boot not active
    return 1
}

if check_secure_boot; then
    echo -e ""
    echo -e "┌───────────────────────────────────────────────────────┐"
    echo -e "│  ${red}[!] WARNING: Secure Boot is ENABLED${plain}                  │"
    echo -e "├───────────────────────────────────────────────────────┤"
    echo -e "│  AmneziaWG kernel module cannot be loaded while       │"
    echo -e "│  Secure Boot is active. AWG tunnels will NOT work.    │"
    echo -e "│                                                       │"
    echo -e "│  To fix this:                                         │"
    echo -e "│  1. Go to your VPS provider control panel             │"
    echo -e "│  2. Find server settings → Disable Secure Boot        │"
    echo -e "│  3. Reboot the server                                 │"
    echo -e "│  4. AmneziaWG will start working automatically        │"
    echo -e "│                                                       │"
    echo -e "│  The panel and all other features work normally.      │"
    echo -e "└───────────────────────────────────────────────────────┘"
fi
