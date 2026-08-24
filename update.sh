#!/bin/bash

red='\033[0;31m'
green='\033[0;32m'
blue='\033[0;34m'
yellow='\033[0;33m'
plain='\033[0m'

xui_folder="${XUI_MAIN_FOLDER:=/usr/local/x-ui}"
xui_service="${XUI_SERVICE:=/etc/systemd/system}"

# Resolve the directory the script lives in. When the script is piped via
# `bash <(curl ...)` this resolves to /dev/fd/N — the local-source detector
# below will then find no source files and fall back to GitHub.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)" || SCRIPT_DIR=""

# Returns 0 when update.sh is being run from inside a cloned 3ax-ui git
# checkout. Mirrors install.sh — see comment there for the BASH_SOURCE
# safety check.
is_local_source_install() {
    local src_name
    src_name="$(basename "${BASH_SOURCE[0]:-}")"
    [[ "$src_name" == "update.sh" ]] || return 1
    [[ -n "$SCRIPT_DIR" ]] || return 1
    [[ -f "$SCRIPT_DIR/update.sh" ]] || return 1
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

# Don't edit this config
b_source="${BASH_SOURCE[0]}"
while [ -h "$b_source" ]; do
    b_dir="$(cd -P "$(dirname "$b_source")" >/dev/null 2>&1 && pwd || pwd -P)"
    b_source="$(readlink "$b_source")"
    [[ $b_source != /* ]] && b_source="$b_dir/$b_source"
done
cur_dir="$(cd -P "$(dirname "$b_source")" >/dev/null 2>&1 && pwd || pwd -P)"
script_name=$(basename "$0")

# Check command exist function
_command_exists() {
    type "$1" &>/dev/null
}

# Fail, log and exit script function
_fail() {
    local msg=${1}
    echo -e "${red}${msg}${plain}"
    exit 2
}

# check root
[[ $EUID -ne 0 ]] && _fail "FATAL ERROR: Please run this script with root privilege."

if _command_exists curl; then
    curl_bin=$(which curl)
else
    _fail "ERROR: Command 'curl' not found."
fi

# Check OS and set release variable
if [[ -f /etc/os-release ]]; then
    source /etc/os-release
    release=$ID
    elif [[ -f /usr/lib/os-release ]]; then
    source /usr/lib/os-release
    release=$ID
else
    _fail "Failed to check the system OS, please contact the author!"
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
        *) echo -e "${red}Unsupported CPU architecture!${plain}" && rm -f "${cur_dir}/${script_name}" >/dev/null 2>&1 && exit 2;;
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

# List certificate identifiers known to acme.sh (main domain of each cert)
acme_cert_domains() {
    [[ -f ~/.acme.sh/acme.sh ]] || return 1
    ~/.acme.sh/acme.sh --list 2>/dev/null | awk 'NR>1 && NF && $1 != "Main_Domain" {print $1}'
}

# Port helpers
is_port_in_use() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -ltn 2>/dev/null | awk -v p=":${port}$" '$4 ~ p {exit 0} END {exit 1}'
        return
    fi
    if command -v netstat >/dev/null 2>&1; then
        netstat -lnt 2>/dev/null | awk -v p=":${port} " '$4 ~ p {exit 0} END {exit 1}'
        return
    fi
    if command -v lsof >/dev/null 2>&1; then
        lsof -nP -iTCP:${port} -sTCP:LISTEN >/dev/null 2>&1 && return 0
    fi
    return 1
}

gen_random_string() {
    local length="$1"
    local random_string=$(LC_ALL=C tr -dc 'a-zA-Z0-9' </dev/urandom | fold -w "$length" | head -n 1)
    echo "$random_string"
}

# Returns 0 if the URL host responds within a short timeout, non-zero on
# connection / DNS / TLS failure. HEAD-only so we don't pull the full
# asset just to probe. No -f: a 404 still means the network path works.
url_reachable() {
    ${curl_bin} --connect-timeout 5 --max-time 10 -sSIL -o /dev/null "$1" 2>/dev/null
}

# Probes URL reachability before downloading. If unreachable, names the
# broken URL and asks the user whether to continue without that resource
# (default Y = skip and proceed). Aborts the script on N.
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

install_base() {
    echo -e "${green}Updating and install dependency packages...${plain}"
    case "${release}" in
        ubuntu | debian | armbian)
            apt-get update >/dev/null 2>&1 && apt-get install -y -q curl tar tzdata socat >/dev/null 2>&1
        ;;
        fedora | amzn | virtuozzo | rhel | almalinux | rocky | ol)
            dnf -y update >/dev/null 2>&1 && dnf install -y -q curl tar tzdata socat >/dev/null 2>&1
        ;;
        centos)
            if [[ "${VERSION_ID}" =~ ^7 ]]; then
                yum -y update >/dev/null 2>&1 && yum install -y -q curl tar tzdata socat >/dev/null 2>&1
            else
                dnf -y update >/dev/null 2>&1 && dnf install -y -q curl tar tzdata socat >/dev/null 2>&1
            fi
        ;;
        arch | manjaro | parch)
            pacman -Syu >/dev/null 2>&1 && pacman -Syu --noconfirm curl tar tzdata socat >/dev/null 2>&1
        ;;
        opensuse-tumbleweed | opensuse-leap)
            zypper refresh >/dev/null 2>&1 && zypper -q install -y curl tar timezone socat >/dev/null 2>&1
        ;;
        alpine)
            apk update >/dev/null 2>&1 && apk add curl tar tzdata socat >/dev/null 2>&1
        ;;
        *)
            apt-get update >/dev/null 2>&1 && apt install -y -q curl tar tzdata socat >/dev/null 2>&1
        ;;
    esac
}

install_acme() {
    echo -e "${green}Installing acme.sh for SSL certificate management...${plain}"
    cd ~ || return 1
    curl -s https://get.acme.sh | sh >/dev/null 2>&1
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
    
    # Install certificate
    ~/.acme.sh/acme.sh --installcert -d ${domain} \
        --key-file /root/cert/${domain}/privkey.pem \
        --fullchain-file /root/cert/${domain}/fullchain.pem \
        --reloadcmd "systemctl restart x-ui" >/dev/null 2>&1
    
    if [ $? -ne 0 ]; then
        echo -e "${yellow}Failed to install certificate${plain}"
        return 1
    fi
    
    # Enable auto-renew
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade >/dev/null 2>&1
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

    # Set reload command for auto-renewal (add || true so it doesn't fail if service stopped)
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
            echo -e "${yellow}Port ${WebPort} is currently in use.${plain}"

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

    chmod 600 ${certDir}/privkey.pem 2>/dev/null
    chmod 644 ${certDir}/fullchain.pem 2>/dev/null

    # Configure panel to use the certificate
    echo -e "${green}Setting certificate paths for the panel...${plain}"
    ${xui_folder}/x-ui cert -webCert "${certDir}/fullchain.pem" -webCertKey "${certDir}/privkey.pem"
    if [ $? -ne 0 ]; then
        echo -e "${yellow}Warning: Could not set certificate paths automatically.${plain}"
        echo -e "${yellow}You may need to set them manually in the panel settings.${plain}"
        echo -e "${yellow}Cert path: ${certDir}/fullchain.pem${plain}"
        echo -e "${yellow}Key path: ${certDir}/privkey.pem${plain}"
    else
        echo -e "${green}Certificate paths set successfully!${plain}"
    fi

    echo -e "${green}IP certificate installed and configured successfully!${plain}"
    echo -e "${green}Certificate valid for ~6 days, auto-renews via acme.sh cron job.${plain}"
    echo -e "${yellow}Panel will automatically restart after each renewal.${plain}"
    return 0
}

# Comprehensive manual SSL certificate issuance via acme.sh
ssl_cert_issue() {
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep 'webBasePath:' | awk -F': ' '{print $2}' | tr -d '[:space:]' | sed 's#^/##')
    local existing_port=$(${xui_folder}/x-ui setting -show true | grep 'port:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
    
    # check for acme.sh first
    if ! command -v ~/.acme.sh/acme.sh &>/dev/null; then
        echo "acme.sh could not be found. Installing now..."
        cd ~ || return 1
        curl -s https://get.acme.sh | sh
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

    # remember the domain for the caller (Access URL)
    ISSUED_DOMAIN="${domain}"

    # check if there already exists a certificate
    if acme_cert_domains | grep -Fxq "${domain}"; then
        echo -e "${yellow}acme.sh already has a certificate for ${domain}:${plain}"
        ~/.acme.sh/acme.sh --list
        read -rp "Re-issue it now (the existing certificate will be overwritten)? (y/n): " reissue
        if [[ "$reissue" != "y" && "$reissue" != "Y" ]]; then
            echo -e "${green}Keeping the existing certificate.${plain}"
            return 0
        fi
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
    local WebPort=""
    read -rp "Please choose which port to use (default is 80): " WebPort
    WebPort="${WebPort// /}"
    WebPort="${WebPort:-80}"
    if ! [[ "${WebPort}" =~ ^[0-9]+$ ]] || ((WebPort < 1 || WebPort > 65535)); then
        echo -e "${yellow}Your input '${WebPort}' is invalid, will use default port 80.${plain}"
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
        ISSUED_DOMAIN=""
        return 1
    else
        echo -e "${green}Issuing certificate succeeded, installing certificates...${plain}"
    fi

    # Setup reload command
    reloadCmd="systemctl restart x-ui || rc-service x-ui restart"
    echo -e "${green}Default --reloadcmd for ACME is: ${yellow}systemctl restart x-ui || rc-service x-ui restart${plain}"
    echo -e "${green}This command will run on every certificate issue and renew.${plain}"
    read -rp "Would you like to modify --reloadcmd for ACME? (y/n): " setReloadcmd
    if [[ "$setReloadcmd" == "y" || "$setReloadcmd" == "Y" ]]; then
        echo -e "\n${green}\t1.${plain} Preset: systemctl reload nginx ; systemctl restart x-ui"
        echo -e "${green}\t2.${plain} Input your own command"
        echo -e "${green}\t0.${plain} Keep default reloadcmd"
        read -rp "Choose an option: " choice
        case "$choice" in
        1)
            if systemctl list-unit-files 2>/dev/null | grep -q '^nginx\.service' || command -v nginx >/dev/null 2>&1; then
                reloadCmd="systemctl reload nginx ; systemctl restart x-ui"
                echo -e "${green}Reloadcmd is: ${reloadCmd}${plain}"
            else
                echo -e "${yellow}nginx is not installed on this system, its reload would fail on every renewal.${plain}"
                echo -e "${green}Keeping default reloadcmd: ${reloadCmd}${plain}"
            fi
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
        echo -e "${red}Installing certificate failed, exiting.${plain}"
        rm -rf ~/.acme.sh/${domain}
        systemctl start x-ui 2>/dev/null || rc-service x-ui start 2>/dev/null
        return 1
    else
        echo -e "${green}Installing certificate succeeded, enabling auto renew...${plain}"
    fi

    # enable auto-renew
    ~/.acme.sh/acme.sh --upgrade --auto-upgrade
    if [ $? -ne 0 ]; then
        echo -e "${yellow}Auto renew setup had issues, certificate details:${plain}"
        ls -lah /root/cert/${domain}/
        chmod 600 $certPath/privkey.pem
        chmod 644 $certPath/fullchain.pem
    else
        echo -e "${green}Auto renew succeeded, certificate details:${plain}"
        ls -lah /root/cert/${domain}/
        chmod 600 $certPath/privkey.pem
        chmod 644 $certPath/fullchain.pem
    fi

    # Restart panel
    systemctl start x-ui 2>/dev/null || rc-service x-ui start 2>/dev/null

    # Prompt user to set panel paths after successful certificate installation
    read -rp "Would you like to set this certificate for the panel? (Y/n): " setPanel
    # Empty answer means yes: a certificate that was just issued for this
    # panel is almost always meant to be used by it, and skipping the step
    # silently leaves the panel serving the previous certificate.
    if [[ -z "$setPanel" || "$setPanel" == "y" || "$setPanel" == "Y" ]]; then
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
# Unified interactive SSL setup (domain or IP)
# Sets global `SSL_HOST` to the chosen domain/IP
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
        ISSUED_DOMAIN=""
        ssl_cert_issue
        # ssl_cert_issue reports the domain it worked on via ISSUED_DOMAIN
        local cert_domain="${ISSUED_DOMAIN}"
        if [[ -n "${cert_domain}" ]]; then
            SSL_HOST="${cert_domain}"
            echo -e "${green}✓ SSL certificate configured successfully with domain: ${cert_domain}${plain}"
        else
            echo -e "${yellow}No certificate was issued; using the IP address for the Access URL${plain}"
            SSL_HOST="${server_ip}"
        fi
        ;;
    2)
        # User chose Let's Encrypt IP certificate option
        echo -e "${green}Using Let's Encrypt for IP certificate (shortlived profile)...${plain}"
        
        # Ask for optional IPv6
        local ipv6_addr=""
        read -rp "Do you have an IPv6 address to include? (leave empty to skip): " ipv6_addr
        ipv6_addr="${ipv6_addr// /}"  # Trim whitespace
        
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
            echo -e "${red}✗ IP certificate setup failed. Please check port 80 is open.${plain}"
            SSL_HOST="${server_ip}"
        fi
        
        # Restart panel after SSL is configured (restart applies new cert settings)
        if [[ $release == "alpine" ]]; then
            rc-service x-ui restart >/dev/null 2>&1
        else
            systemctl restart x-ui >/dev/null 2>&1
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

# Localhost-only debug update. Plain HTTP, listen=127.0.0.1, port default
# 8080 (kept if already set), no SSL prompt, no public-IP detection.
config_debug_mode_after_update() {
    echo -e "${yellow}x-ui settings (debug mode):${plain}"
    ${xui_folder}/x-ui setting -show true
    ${xui_folder}/x-ui migrate

    local existing_port=$(${xui_folder}/x-ui setting -show true | grep -Eo 'port: .+' | awk '{print $2}')
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep -Eo 'webBasePath: .+' | awk '{print $2}' | sed 's#^/##')

    # Prefer the port the user picked at the start of the update; only
    # fall back to whatever was previously configured if they didn't
    # answer the prompt (e.g. running with an older XUI_DEBUG_PORT env).
    local desired_port="${XUI_DEBUG_PORT:-${existing_port}}"
    if [[ -z "${desired_port}" || "${desired_port}" == "0" ]]; then
        desired_port=8080
    fi
    if [[ "${desired_port}" != "${existing_port}" ]]; then
        ${xui_folder}/x-ui setting -port "${desired_port}"
        existing_port="${desired_port}"
    fi
    if [[ ${#existing_webBasePath} -lt 4 ]]; then
        existing_webBasePath=$(gen_random_string 18)
        ${xui_folder}/x-ui setting -webBasePath "${existing_webBasePath}"
    fi

    # Force loopback bind on every update so a previously-public install
    # can be safely flipped to debug mode.
    ${xui_folder}/x-ui setting -listenIP "127.0.0.1"

    echo ""
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}  Panel updated in DEBUG / localhost mode    ${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"
    echo -e "${green}Port:        ${existing_port}${plain}"
    echo -e "${green}WebBasePath: ${existing_webBasePath}${plain}"
    echo -e "${green}Listen:      127.0.0.1 (loopback only)${plain}"
    echo -e "${green}Access URL:  http://127.0.0.1:${existing_port}/${existing_webBasePath}${plain}"
    echo -e "${green}             http://localhost:${existing_port}/${existing_webBasePath}${plain}"
    echo -e "${green}═══════════════════════════════════════════${plain}"
}

config_after_update() {
    if [[ "${XUI_DEBUG_MODE:-}" == "1" ]]; then
        config_debug_mode_after_update
        return
    fi

    echo -e "${yellow}x-ui settings:${plain}"
    ${xui_folder}/x-ui setting -show true
    ${xui_folder}/x-ui migrate
    
    # Properly detect empty cert by checking if cert: line exists and has content after it
    local existing_cert=$(${xui_folder}/x-ui setting -getCert true 2>/dev/null | grep 'cert:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
    local existing_port=$(${xui_folder}/x-ui setting -show true | grep -Eo 'port: .+' | awk '{print $2}')
    local existing_webBasePath=$(${xui_folder}/x-ui setting -show true | grep -Eo 'webBasePath: .+' | awk '{print $2}' | sed 's#^/##')
    
    # Get server IP
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
    
    # Handle missing/short webBasePath
    if [[ ${#existing_webBasePath} -lt 4 ]]; then
        echo -e "${yellow}WebBasePath is missing or too short. Generating a new one...${plain}"
        local config_webBasePath=$(gen_random_string 18)
        ${xui_folder}/x-ui setting -webBasePath "${config_webBasePath}"
        existing_webBasePath="${config_webBasePath}"
        echo -e "${green}New WebBasePath: ${config_webBasePath}${plain}"
    fi
    
    # Check and prompt for SSL if missing
    if [[ -z "$existing_cert" ]]; then
        echo ""
        echo -e "${red}═══════════════════════════════════════════${plain}"
        echo -e "${red}      ⚠ NO SSL CERTIFICATE DETECTED ⚠     ${plain}"
        echo -e "${red}═══════════════════════════════════════════${plain}"
        echo -e "${yellow}For security, SSL certificate is MANDATORY for all panels.${plain}"
        echo -e "${yellow}Let's Encrypt now supports both domains and IP addresses!${plain}"
        echo ""
        
        if [[ -z "${server_ip}" ]]; then
            echo -e "${red}Failed to detect server IP${plain}"
            echo -e "${yellow}Please configure SSL manually using: x-ui${plain}"
            return
        fi
        
        # Prompt and setup SSL (domain or IP)
        prompt_and_setup_ssl "${existing_port}" "${existing_webBasePath}" "${server_ip}"
        
        echo ""
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}     Panel Access Information              ${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}Access URL: https://${SSL_HOST}:${existing_port}/${existing_webBasePath}${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${yellow}⚠ SSL Certificate: Enabled and configured${plain}"
    else
        echo -e "${green}SSL certificate is already configured${plain}"
        # Show access URL with existing certificate. IP certificates are stored
        # in /root/cert/ip, so the directory name is the literal "ip" — show the
        # real detected server IP instead of printing "https://ip:...".
        local cert_domain=$(basename "$(dirname "$existing_cert")")
        local access_host="$cert_domain"
        if [[ "$cert_domain" == "ip" ]]; then
            access_host="${server_ip:-$cert_domain}"
        fi
        echo ""
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}     Panel Access Information              ${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
        echo -e "${green}Access URL: https://${access_host}:${existing_port}/${existing_webBasePath}${plain}"
        echo -e "${green}═══════════════════════════════════════════${plain}"
    fi
}

# Translates the panel's arch label to xray-core's release naming so we can
# fetch the right Xray-linux-{ARCH}.zip from XTLS/Xray-core releases.
# install_nginx puts nginx on the server so the panel can consolidate every TLS
# protocol onto port 443. It is installed unconditionally, before the panel, for
# the same reason AmneziaWG is: the panel decides on first start what it can
# offer, and a front-end that appears only after the next update is a front-end
# nobody finds.
#
# Nothing here fails the installation. A server without nginx keeps working
# exactly as before — it simply cannot hide its protocols behind one port.
install_nginx() {
    if command -v nginx &>/dev/null; then
        echo -e "${green}nginx already installed: $(nginx -v 2>&1 | sed 's|.*nginx/||')${plain}"
    else
        echo -e "${green}Installing nginx...${plain}"
        case "${release}" in
            ubuntu | debian | armbian)
                # On Debian and its derivatives the stream module is a separate
                # package, and without it nginx cannot split 443 by SNI at all.
                apt-get install -y -q nginx libnginx-mod-stream 2>/dev/null ||
                    apt-get install -y -q nginx 2>/dev/null || true
                ;;
            fedora | amzn | rhel | almalinux | rocky | ol | centos)
                dnf install -y nginx nginx-mod-stream 2>/dev/null ||
                    dnf install -y nginx 2>/dev/null ||
                    yum install -y nginx 2>/dev/null || true
                ;;
            arch | manjaro | parch)
                pacman -Syu --noconfirm nginx 2>/dev/null || true
                ;;
            alpine)
                apk add nginx nginx-mod-stream 2>/dev/null || apk add nginx 2>/dev/null || true
                ;;
            *)
                echo -e "${yellow}Unknown OS — install nginx by hand to use the «everything on 443» modes.${plain}"
                ;;
        esac
    fi

    if ! command -v nginx &>/dev/null; then
        echo -e "${yellow}nginx was not installed. The panel works as before; the Nginx page will${plain}"
        echo -e "${yellow}stay unavailable until nginx is installed.${plain}"
        return
    fi

    # The panel owns this directory: a distro's nginx.conf includes conf.d from
    # inside http {}, where a stream block is a syntax error, so the stream part
    # of the config needs a home of its own.
    mkdir -p /etc/nginx/stream-enabled /usr/local/x-ui/www

    if ! nginx -V 2>&1 | grep -q -- '--with-stream' &&
        ! ls /etc/nginx/modules-enabled/*stream*.conf &>/dev/null; then
        echo -e "${yellow}This nginx has no stream module, so port 443 cannot be split by server name.${plain}"
        case "${release}" in
            ubuntu | debian | armbian) echo -e "${yellow}  Fix: apt-get install -y libnginx-mod-stream${plain}" ;;
            alpine) echo -e "${yellow}  Fix: apk add nginx-mod-stream${plain}" ;;
            *) echo -e "${yellow}  Install the nginx stream module for your distribution.${plain}" ;;
        esac
    fi

    # A configuration the panel generated earlier may not be accepted by a newer
    # nginx. Saying so here is the difference between a five-minute fix and a
    # server where 443 is quietly down after an update.
    if ! nginx -t &>/dev/null; then
        echo -e "${red}nginx refuses the current configuration:${plain}"
        nginx -t 2>&1 | sed 's/^/    /'
        echo -e "${yellow}Port 443 stays down until this is fixed. The panel rewrites its own part${plain}"
        echo -e "${yellow}of the config from the Nginx page — reapplying the mode there is usually enough.${plain}"
        return
    fi

    # A server that reboots without nginx comes back with 443 shut and every
    # protocol behind it unreachable.
    if command -v systemctl &>/dev/null; then
        systemctl enable nginx &>/dev/null || true
        systemctl start nginx &>/dev/null || systemctl reload nginx &>/dev/null || true
    elif command -v rc-update &>/dev/null; then
        rc-update add nginx default &>/dev/null || true
        rc-service nginx start &>/dev/null || true
    fi

    echo -e "${green}nginx: $(nginx -v 2>&1 | sed 's|.*nginx/||')${plain}"
}

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

# Translates the panel's arch label to the filename the panel uses for the
# bundled xray binary (panel looks up bin/xray-linux-{FNAME}).
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

# Pinned mtg (MTProto FakeTLS sidecar, github.com/9seconds/mtg) version.
MTG_VER="2.2.8"

# mtg release-asset arch (empty = no prebuilt binary; s390x has none, armv5
# falls back to the armv6 build).
mtg_release_arch() {
    case "$(arch)" in
        amd64) echo "amd64" ;;
        386) echo "386" ;;
        arm64) echo "arm64" ;;
        armv7) echo "armv7" ;;
        armv6|armv5) echo "armv6" ;;
        *) echo "" ;;
    esac
}

# On-disk filename Go's mtproto package looks up: bin/mtg-linux-{FNAME}
# (FNAME == runtime.GOARCH, so all 32-bit arm collapse to "arm").
mtg_panel_arch() {
    case "$(arch)" in
        amd64) echo "amd64" ;;
        386) echo "386" ;;
        arm64) echo "arm64" ;;
        armv7|armv6|armv5) echo "arm" ;;
        *) echo "" ;;
    esac
}

# Ensures the mtg sidecar is present in the given bin dir as mtg-linux-{FNAME}.
# No-op when already present (preserved across updates). Fully non-fatal: any
# failure prints a notice and returns 0 — MTProto inbounds just won't start.
# mtg-multi (dolonet/mtg-multi) = multi-user MTProto fork; prebuilt only for
# linux amd64/arm64. Empty otherwise (fall back to single-secret mtg).
mtg_multi_arch() {
    case "$(arch)" in
        amd64) echo "amd64" ;;
        arm64) echo "arm64" ;;
        *) echo "" ;;
    esac
}

# Installs latest mtg-multi as bin/mtg-multi-linux-{FNAME}; returns 1 on failure.
install_mtg_multi() {
    local target_bin_dir="$1"
    local mm_arch mm_fname mm_ver mm_url tmp_tgz tmp_dir extracted installed_bin installed_ver
    mm_arch=$(mtg_multi_arch)
    mm_fname=$(mtg_panel_arch)
    [[ -z "$mm_arch" || -z "$mm_fname" ]] && return 1
    installed_bin="$target_bin_dir/mtg-multi-linux-${mm_fname}"

    # The user opted for "always latest": resolve the newest release tag and, if a
    # binary is already installed, upgrade it only when it is out of date.
    mm_ver=$(${curl_bin:-curl} -4 -Ls "https://api.github.com/repos/dolonet/mtg-multi/releases/latest" 2>/dev/null \
        | grep '"tag_name":' | sed -E 's/.*"v?([^"]+)".*/\1/' | head -n1)
    if [[ -f "$installed_bin" ]]; then
        chmod +x "$installed_bin" >/dev/null 2>&1
        # Can't resolve the latest version (e.g. API rate limit) — keep what we have.
        [[ -z "$mm_ver" ]] && return 0
        installed_ver=$("$installed_bin" --version 2>/dev/null | awk '{print $1}')
        if [[ "$installed_ver" == "$mm_ver" ]]; then
            return 0
        fi
        echo -e "${green}Updating mtg-multi ${installed_ver:-unknown} -> ${mm_ver}...${plain}"
    fi
    [[ -z "$mm_ver" ]] && return 1
    mm_url="https://github.com/dolonet/mtg-multi/releases/download/v${mm_ver}/mtg-multi-${mm_ver}-linux-${mm_arch}.tar.gz"
    echo -e "${green}Downloading mtg-multi (multi-user MTProto)...${plain}"
    tmp_tgz="/tmp/mtgmulti.$$.tar.gz"
    tmp_dir="/tmp/mtgmulti.$$.d"
    if ! ${curl_bin:-curl} -4fLRo "$tmp_tgz" "$mm_url" >/dev/null 2>&1; then
        rm -f "$tmp_tgz" >/dev/null 2>&1
        return 1
    fi
    mkdir -p "$tmp_dir" "$target_bin_dir" >/dev/null 2>&1
    if tar -xzf "$tmp_tgz" -C "$tmp_dir" >/dev/null 2>&1; then
        extracted=$(find "$tmp_dir" -type f -name mtg-multi 2>/dev/null | head -n1)
        if [[ -n "$extracted" ]]; then
            mv -f "$extracted" "$target_bin_dir/mtg-multi-linux-${mm_fname}" >/dev/null 2>&1
            chmod +x "$target_bin_dir/mtg-multi-linux-${mm_fname}" >/dev/null 2>&1
            rm -f "$target_bin_dir/mtg-linux-${mm_fname}" >/dev/null 2>&1
            echo -e "${green}mtg-multi installed (multi-user MTProto).${plain}"
            rm -rf "$tmp_tgz" "$tmp_dir" >/dev/null 2>&1
            return 0
        fi
    fi
    rm -rf "$tmp_tgz" "$tmp_dir" >/dev/null 2>&1
    return 1
}

install_mtg() {
    local target_bin_dir="$1"
    local mtg_arch mtg_fname mtg_url tmp_tgz tmp_dir extracted
    # Prefer the multi-user mtg-multi fork where a prebuilt binary exists.
    if install_mtg_multi "$target_bin_dir"; then
        return 0
    fi
    mtg_arch=$(mtg_release_arch)
    mtg_fname=$(mtg_panel_arch)
    if [[ -z "$mtg_arch" || -z "$mtg_fname" ]]; then
        return 0
    fi
    if [[ -f "$target_bin_dir/mtg-linux-${mtg_fname}" ]]; then
        chmod +x "$target_bin_dir/mtg-linux-${mtg_fname}" >/dev/null 2>&1
        return 0
    fi
    mtg_url="https://github.com/9seconds/mtg/releases/download/v${MTG_VER}/mtg-${MTG_VER}-linux-${mtg_arch}.tar.gz"
    echo -e "${green}Downloading mtg (MTProto sidecar)...${plain}"
    tmp_tgz="/tmp/mtg.$$.tar.gz"
    tmp_dir="/tmp/mtg.$$.d"
    if ! ${curl_bin:-curl} -4fLRo "$tmp_tgz" "$mtg_url" >/dev/null 2>&1; then
        rm -f "$tmp_tgz" >/dev/null 2>&1
        echo -e "${yellow}Could not download mtg — MTProto proxies will be unavailable.${plain}"
        return 0
    fi
    mkdir -p "$tmp_dir" "$target_bin_dir" >/dev/null 2>&1
    if tar -xzf "$tmp_tgz" -C "$tmp_dir" >/dev/null 2>&1; then
        extracted=$(find "$tmp_dir" -type f -name mtg 2>/dev/null | head -n1)
        if [[ -n "$extracted" ]]; then
            mv -f "$extracted" "$target_bin_dir/mtg-linux-${mtg_fname}" >/dev/null 2>&1
            chmod +x "$target_bin_dir/mtg-linux-${mtg_fname}" >/dev/null 2>&1
            echo -e "${green}mtg installed as bin/mtg-linux-${mtg_fname}.${plain}"
        fi
    fi
    rm -rf "$tmp_tgz" "$tmp_dir" >/dev/null 2>&1
    return 0
}

# Downloads xray binary + geo data files into the given target directory.
# Mirrors the logic in DockerInit.sh — same xray version (v26.3.27), same
# geo-data sources.
download_xray_and_geo() {
    local target_bin_dir="$1"
    local xray_arch xray_fname xray_url
    xray_arch=$(xray_release_arch)
    xray_fname=$(xray_panel_arch)
    if [[ -z "$xray_arch" || -z "$xray_fname" ]]; then
        echo -e "${red}No prebuilt xray-core for arch $(arch).${plain}"
        return 1
    fi
    if ! _command_exists unzip; then
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

    if ! check_url_or_skip "$xray_url" "xray-core binary"; then
        echo -e "${red}Cannot proceed without xray-core — aborting xray bundle download.${plain}"
        return 1
    fi
    echo -e "${green}Downloading xray-core ${xray_url}...${plain}"
    if ! ${curl_bin} -4fLRo "$tmp_zip" "$xray_url"; then
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

    echo -e "${green}Downloading geo data...${plain}"
    local geo_url
    geo_url="https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat"
    check_url_or_skip "$geo_url" "geoip.dat (Loyalsoldier)" && \
        ${curl_bin} -4sfLRo "$target_bin_dir/geoip.dat" "$geo_url"
    geo_url="https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat"
    check_url_or_skip "$geo_url" "geosite.dat (Loyalsoldier)" && \
        ${curl_bin} -4sfLRo "$target_bin_dir/geosite.dat" "$geo_url"
    geo_url="https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geoip.dat"
    check_url_or_skip "$geo_url" "geoip_IR.dat (Iran rules)" && \
        ${curl_bin} -4sfLRo "$target_bin_dir/geoip_IR.dat" "$geo_url"
    geo_url="https://github.com/chocolate4u/Iran-v2ray-rules/releases/latest/download/geosite.dat"
    check_url_or_skip "$geo_url" "geosite_IR.dat (Iran rules)" && \
        ${curl_bin} -4sfLRo "$target_bin_dir/geosite_IR.dat" "$geo_url"
    geo_url="https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geoip.dat"
    check_url_or_skip "$geo_url" "geoip_RU.dat (Russia rules)" && \
        ${curl_bin} -4sfLRo "$target_bin_dir/geoip_RU.dat" "$geo_url"
    geo_url="https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/geosite.dat"
    check_url_or_skip "$geo_url" "geosite_RU.dat (Russia rules)" && \
        ${curl_bin} -4sfLRo "$target_bin_dir/geosite_RU.dat" "$geo_url"
    return 0
}

# Wrapper around download_xray_and_geo that, in debug mode only, reuses
# an existing xray + geo bundle from well-known cache locations
# (${xui_folder}/bin, $SCRIPT_DIR/build/bin, $SCRIPT_DIR/target/bin)
# instead of re-downloading. Production updates always pull fresh.
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
# default makes `go build` self-bootstrap the version pinned in go.mod, so we
# only need a recent-enough bootstrap here.
ensure_go() {
    local need_install=1
    if _command_exists go; then
        local v
        v=$(go env GOVERSION 2>/dev/null | sed -E 's/^go//')
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
            echo -e "${red}No prebuilt Go binary for arch $(arch).${plain}"
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
    if ! ${curl_bin} -4fLRo "$tmp_tgz" "$go_url"; then
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
    if ! _command_exists go; then
        echo -e "${red}Go installed but not on PATH.${plain}"
        return 1
    fi
    echo -e "${green}Go installed: $(go version)${plain}"
    return 0
}

# Builds the panel binary from the local source tree and assembles the same
# directory layout the GitHub release tarball would extract into. After this
# returns successfully, update_x-ui's existing post-extract logic (chmod,
# service install, etc.) takes over unchanged with CWD = ${xui_folder}.
update_x-ui_from_source() {
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

    # Replace only the files we own. x-ui.db (panel database) and bin/
    # (xray + geo data) survive across updates so we don't wipe user data
    # or trigger pointless multi-MB redownloads.
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
        echo -e "${red}Failed to fetch xray-core for the local-source update.${plain}"
        return 1
    fi

    tag_version="${build_version}"
    return 0
}

update_x-ui() {
    cd ${xui_folder%/x-ui}/
    local xray_backup=""
    local xray_backup_name=""

    if [ -f "${xui_folder}/x-ui" ]; then
        current_xui_version=$(${xui_folder}/x-ui -v)
        echo -e "${green}Current x-ui version: ${current_xui_version}${plain}"
    else
        _fail "ERROR: Current x-ui version: unknown"
    fi

    # Local-source update path — build from cloned repo, skip the GitHub
    # download. Mirrors the GitHub-release flow's pre-/post-install hooks
    # (xray binary preservation, service-unit reinstall, x-ui.sh reinstall,
    # owner / permission fixups, config_after_update).
    if is_local_source_install; then
        echo -e "${green}Preserving xray binary before update...${plain}"
        if [[ -e ${xui_folder}/ ]]; then
            for candidate in "${xui_folder}"/bin/xray-linux-*; do
                if [[ -f "$candidate" ]]; then
                    xray_backup_name=$(basename "$candidate")
                    xray_backup="/tmp/${xray_backup_name}.xui-update.$$"
                    if cp -f "$candidate" "$xray_backup" >/dev/null 2>&1; then
                        echo -e "${green}Preserving existing Xray core binary: ${xray_backup_name}${plain}"
                    else
                        xray_backup=""
                        xray_backup_name=""
                    fi
                    break
                fi
            done

            echo -e "${green}Stopping x-ui...${plain}"
            if [[ $release == "alpine" ]]; then
                rc-service x-ui stop >/dev/null 2>&1
                rc-update del x-ui >/dev/null 2>&1
                rm -f /etc/init.d/x-ui >/dev/null 2>&1
            else
                systemctl stop x-ui >/dev/null 2>&1
                systemctl disable x-ui >/dev/null 2>&1
                rm ${xui_service}/x-ui.service -f >/dev/null 2>&1
                systemctl daemon-reload >/dev/null 2>&1
            fi
        fi

        if update_x-ui_from_source; then
            cd "${xui_folder}" >/dev/null 2>&1
            chmod +x x-ui >/dev/null 2>&1
            if [[ $(arch) == "armv5" || $(arch) == "armv6" || $(arch) == "armv7" ]]; then
                mv bin/xray-linux-$(arch) bin/xray-linux-arm >/dev/null 2>&1
                chmod +x bin/xray-linux-arm >/dev/null 2>&1
            fi
            chmod +x x-ui >/dev/null 2>&1
            [ -f bin/xray-linux-$(arch) ] && chmod +x bin/xray-linux-$(arch) >/dev/null 2>&1
            if [[ -n "$xray_backup" && -n "$xray_backup_name" && -f "$xray_backup" ]]; then
                cp -f "$xray_backup" "bin/${xray_backup_name}" >/dev/null 2>&1 && \
                    chmod +x "bin/${xray_backup_name}" >/dev/null 2>&1
                rm -f "$xray_backup" >/dev/null 2>&1
            fi
            # Ensure the mtg MTProto sidecar is present (the local-source build
            # only fetches xray). No-op if already installed; non-fatal.
            install_mtg "${xui_folder}/bin"

            cp -f "${xui_folder}/x-ui.sh" /usr/bin/x-ui >/dev/null 2>&1
            chmod +x ${xui_folder}/x-ui.sh >/dev/null 2>&1
            chmod +x /usr/bin/x-ui >/dev/null 2>&1
            mkdir -p /var/log/x-ui >/dev/null 2>&1
            chown -R root:root ${xui_folder} >/dev/null 2>&1
            [ -f "${xui_folder}/bin/config.json" ] && chmod 640 ${xui_folder}/bin/config.json >/dev/null 2>&1

            update_x-ui_install_service
            config_after_update
            update_x-ui_print_footer
            return
        fi
        echo -e "${yellow}Local-source update did not complete — falling back to GitHub release.${plain}"
        # Fall through to the GitHub-release flow.
    fi

    echo -e "${green}Downloading new x-ui version...${plain}"

    if [[ "$1" == "--beta" || "$1" == "--pre" ]]; then
        tag_version=$(${curl_bin} -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -1)
        if [[ ! -n "$tag_version" ]]; then
            echo -e "${yellow}Trying to fetch version with IPv4...${plain}"
            tag_version=$(${curl_bin} -4 -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -1)
        fi
        echo -e "Got x-ui latest pre-release version: ${tag_version}, beginning the installation..."
    else
        tag_version=$(${curl_bin} -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases/latest" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ ! -n "$tag_version" ]]; then
            echo -e "${yellow}Trying to fetch version with IPv4...${plain}"
            tag_version=$(${curl_bin} -4 -Ls "https://api.github.com/repos/coinman-dev/3ax-ui/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        fi
        echo -e "Got x-ui latest version: ${tag_version}, beginning the installation..."
    fi
    if [[ ! -n "$tag_version" ]]; then
        _fail "ERROR: Failed to fetch x-ui version, it may be due to GitHub API restrictions, please try it later"
    fi
    ${curl_bin} -fLRo ${xui_folder}-linux-$(arch).tar.gz https://github.com/coinman-dev/3ax-ui/releases/download/${tag_version}/x-ui-linux-$(arch).tar.gz 2>/dev/null
    if [[ $? -ne 0 ]]; then
        echo -e "${yellow}Trying to fetch version with IPv4...${plain}"
        ${curl_bin} -4fLRo ${xui_folder}-linux-$(arch).tar.gz https://github.com/coinman-dev/3ax-ui/releases/download/${tag_version}/x-ui-linux-$(arch).tar.gz 2>/dev/null
        if [[ $? -ne 0 ]]; then
            _fail "ERROR: Failed to download x-ui, please be sure that your server can access GitHub"
        fi
    fi
    
    # Verify the archive BEFORE anything is removed. An interrupted download
    # used to reach the install step below, where tar failed with its output
    # discarded and `cd x-ui` silently did nothing — the old version was already
    # deleted by then, so the panel was left with no binary at all and every
    # later step reported "No such file or directory".
    if ! tar -tzf x-ui-linux-$(arch).tar.gz >/dev/null 2>&1; then
        rm x-ui-linux-$(arch).tar.gz -f >/dev/null 2>&1
        _fail "ERROR: the downloaded archive is corrupt (interrupted download?). Nothing has been changed — run the update again."
    fi
    if ! tar -tzf x-ui-linux-$(arch).tar.gz 2>/dev/null | grep -qx "x-ui/x-ui"; then
        rm x-ui-linux-$(arch).tar.gz -f >/dev/null 2>&1
        _fail "ERROR: the downloaded archive does not contain the x-ui binary. Nothing has been changed."
    fi

    if [[ -e ${xui_folder}/ ]]; then
        for candidate in "${xui_folder}"/bin/xray-linux-*; do
            if [[ -f "$candidate" ]]; then
                xray_backup_name=$(basename "$candidate")
                xray_backup="/tmp/${xray_backup_name}.xui-update.$$"
                if cp -f "$candidate" "$xray_backup" >/dev/null 2>&1; then
                    echo -e "${green}Preserving existing Xray core binary: ${xray_backup_name}${plain}"
                else
                    xray_backup=""
                    xray_backup_name=""
                    echo -e "${yellow}Failed to preserve existing Xray core binary; bundled core may be used after update.${plain}"
                fi
                break
            fi
        done

        echo -e "${green}Stopping x-ui...${plain}"
        if [[ $release == "alpine" ]]; then
            if [ -f "/etc/init.d/x-ui" ]; then
                rc-service x-ui stop >/dev/null 2>&1
                rc-update del x-ui >/dev/null 2>&1
                echo -e "${green}Removing old service unit version...${plain}"
                rm -f /etc/init.d/x-ui >/dev/null 2>&1
            else
                rm x-ui-linux-$(arch).tar.gz -f >/dev/null 2>&1
                _fail "ERROR: x-ui service unit not installed."
            fi
        else
            if [ -f "${xui_service}/x-ui.service" ]; then
                systemctl stop x-ui >/dev/null 2>&1
                systemctl disable x-ui >/dev/null 2>&1
                echo -e "${green}Removing old systemd unit version...${plain}"
                rm ${xui_service}/x-ui.service -f >/dev/null 2>&1
                systemctl daemon-reload >/dev/null 2>&1
            else
                rm x-ui-linux-$(arch).tar.gz -f >/dev/null 2>&1
                _fail "ERROR: x-ui systemd unit not installed."
            fi
        fi
        echo -e "${green}Removing old x-ui version...${plain}"
        rm ${xui_folder} -f >/dev/null 2>&1
        rm ${xui_folder}/x-ui.service -f >/dev/null 2>&1
        rm ${xui_folder}/x-ui.service.debian -f >/dev/null 2>&1
        rm ${xui_folder}/x-ui.service.arch -f >/dev/null 2>&1
        rm ${xui_folder}/x-ui.service.rhel -f >/dev/null 2>&1
        rm ${xui_folder}/x-ui -f >/dev/null 2>&1
        rm ${xui_folder}/x-ui.sh -f >/dev/null 2>&1
        echo -e "${green}Removing old xray version...${plain}"
        rm ${xui_folder}/bin/xray-linux-amd64 -f >/dev/null 2>&1
        echo -e "${green}Removing old README and LICENSE file...${plain}"
        rm ${xui_folder}/bin/README.md -f >/dev/null 2>&1
        rm ${xui_folder}/bin/LICENSE -f >/dev/null 2>&1
    else
        rm x-ui-linux-$(arch).tar.gz -f >/dev/null 2>&1
        _fail "ERROR: x-ui not installed."
    fi
    
    echo -e "${green}Installing new x-ui version...${plain}"
    # Both steps are load-bearing and used to fail silently: the old version is
    # gone at this point, so anything that goes wrong here has to say so.
    if ! tar zxf x-ui-linux-$(arch).tar.gz >/dev/null 2>&1; then
        _fail "ERROR: failed to unpack x-ui-linux-$(arch).tar.gz. The panel binary is missing — run the update again to restore it."
    fi
    rm x-ui-linux-$(arch).tar.gz -f >/dev/null 2>&1
    cd x-ui || _fail "ERROR: the unpacked x-ui folder is missing. The panel binary is missing — run the update again to restore it."
    chmod +x x-ui >/dev/null 2>&1
    
    # Check the system's architecture and rename the file accordingly
    if [[ $(arch) == "armv5" || $(arch) == "armv6" || $(arch) == "armv7" ]]; then
        mv bin/xray-linux-$(arch) bin/xray-linux-arm >/dev/null 2>&1
        chmod +x bin/xray-linux-arm >/dev/null 2>&1
        mv bin/mtg-linux-$(arch) bin/mtg-linux-arm >/dev/null 2>&1
        chmod +x bin/mtg-linux-arm >/dev/null 2>&1
    fi

    chmod +x x-ui >/dev/null 2>&1
    [ -f bin/xray-linux-$(arch) ] && chmod +x bin/xray-linux-$(arch) >/dev/null 2>&1
    # Ensure the mtg MTProto sidecar is present (covers updates from a tarball
    # that predates MTProto support). No-op if already shipped; non-fatal.
    install_mtg "bin"
    if [[ -n "$xray_backup" && -n "$xray_backup_name" && -f "$xray_backup" ]]; then
        cp -f "$xray_backup" "bin/${xray_backup_name}" >/dev/null 2>&1
        if [[ $? -eq 0 ]]; then
            chmod +x "bin/${xray_backup_name}" >/dev/null 2>&1
            echo -e "${green}Restored existing Xray core binary: ${xray_backup_name}${plain}"
        else
            echo -e "${yellow}Failed to restore existing Xray core binary; using bundled version.${plain}"
        fi
        rm -f "$xray_backup" >/dev/null 2>&1
    fi
    
    echo -e "${green}Downloading and installing x-ui.sh script...${plain}"
    ${curl_bin} -fLRo /usr/bin/x-ui https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.sh >/dev/null 2>&1
    if [[ $? -ne 0 ]]; then
        echo -e "${yellow}Trying to fetch x-ui with IPv4...${plain}"
        ${curl_bin} -4fLRo /usr/bin/x-ui https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.sh >/dev/null 2>&1
        if [[ $? -ne 0 ]]; then
            _fail "ERROR: Failed to download x-ui.sh script, please be sure that your server can access GitHub"
        fi
    fi
    
    chmod +x ${xui_folder}/x-ui.sh >/dev/null 2>&1
    chmod +x /usr/bin/x-ui >/dev/null 2>&1
    mkdir -p /var/log/x-ui >/dev/null 2>&1
    
    echo -e "${green}Changing owner...${plain}"
    chown -R root:root ${xui_folder} >/dev/null 2>&1
    
    if [ -f "${xui_folder}/bin/config.json" ]; then
        echo -e "${green}Changing on config file permissions...${plain}"
        chmod 640 ${xui_folder}/bin/config.json >/dev/null 2>&1
    fi
    
    update_x-ui_install_service
    config_after_update
    update_x-ui_print_footer
}

# Installs and starts the OS service unit during update. Prefers files
# embedded in ${xui_folder}/ (delivered both by the release tarball and by
# the local-source build); falls back to GitHub raw if missing.
update_x-ui_install_service() {
    if [[ $release == "alpine" ]]; then
        if [ -f "${xui_folder}/x-ui.rc" ]; then
            cp -f "${xui_folder}/x-ui.rc" /etc/init.d/x-ui >/dev/null 2>&1
        else
            ${curl_bin} -fLRo /etc/init.d/x-ui https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.rc >/dev/null 2>&1
            if [[ $? -ne 0 ]]; then
                ${curl_bin} -4fLRo /etc/init.d/x-ui https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.rc >/dev/null 2>&1
                [[ $? -ne 0 ]] && _fail "ERROR: Failed to download startup unit x-ui.rc"
            fi
        fi
        chmod +x /etc/init.d/x-ui >/dev/null 2>&1
        chown root:root /etc/init.d/x-ui >/dev/null 2>&1
        rc-update add x-ui >/dev/null 2>&1
        rc-service x-ui start >/dev/null 2>&1
        return
    fi

    # systemd path
    local service_installed=false
    if [ -f "${xui_folder}/x-ui.service" ]; then
        echo -e "${green}Installing systemd unit...${plain}"
        cp -f "${xui_folder}/x-ui.service" ${xui_service}/ >/dev/null 2>&1 && service_installed=true
    fi
    if [ "$service_installed" = false ]; then
        case "${release}" in
            ubuntu | debian | armbian)
                if [ -f "${xui_folder}/x-ui.service.debian" ]; then
                    echo -e "${green}Installing debian-like systemd unit...${plain}"
                    cp -f "${xui_folder}/x-ui.service.debian" ${xui_service}/x-ui.service >/dev/null 2>&1 && service_installed=true
                fi
            ;;
            arch | manjaro | parch)
                if [ -f "${xui_folder}/x-ui.service.arch" ]; then
                    echo -e "${green}Installing arch-like systemd unit...${plain}"
                    cp -f "${xui_folder}/x-ui.service.arch" ${xui_service}/x-ui.service >/dev/null 2>&1 && service_installed=true
                fi
            ;;
            *)
                if [ -f "${xui_folder}/x-ui.service.rhel" ]; then
                    echo -e "${green}Installing rhel-like systemd unit...${plain}"
                    cp -f "${xui_folder}/x-ui.service.rhel" ${xui_service}/x-ui.service >/dev/null 2>&1 && service_installed=true
                fi
            ;;
        esac
    fi
    if [ "$service_installed" = false ]; then
        echo -e "${yellow}Service files not found locally, downloading from GitHub...${plain}"
        case "${release}" in
            ubuntu | debian | armbian)
                ${curl_bin} -4fLRo ${xui_service}/x-ui.service https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.service.debian >/dev/null 2>&1
            ;;
            arch | manjaro | parch)
                ${curl_bin} -4fLRo ${xui_service}/x-ui.service https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.service.arch >/dev/null 2>&1
            ;;
            *)
                ${curl_bin} -4fLRo ${xui_service}/x-ui.service https://raw.githubusercontent.com/coinman-dev/3ax-ui/${REPO_BRANCH}/x-ui.service.rhel >/dev/null 2>&1
            ;;
        esac
        [[ $? -ne 0 ]] && _fail "ERROR: Failed to install x-ui.service from GitHub"
    fi
    chown root:root ${xui_service}/x-ui.service >/dev/null 2>&1
    chmod 644 ${xui_service}/x-ui.service >/dev/null 2>&1
    systemctl daemon-reload >/dev/null 2>&1
    systemctl enable x-ui >/dev/null 2>&1
    systemctl start x-ui >/dev/null 2>&1
}

update_x-ui_print_footer() {
    echo -e "${green}x-ui ${tag_version}${plain} updating finished, it is running now..."
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

# prune_stale_amneziawg_dkms drops every amneziawg build in the DKMS tree except
# the newest one.
#
# Upgrading the package normally removes its own predecessor, but a version left
# behind by an interrupted upgrade — or a module someone built by hand before
# the panel was installed — stays in the tree, gets rebuilt for every new kernel
# from then on, and leaves two amneziawg.ko for depmod to choose between. The
# symptom is a tunnel that works until a kernel update and then loads the wrong
# module.
prune_stale_amneziawg_dkms() {
    command -v dkms &>/dev/null || return 0

    local versions count newest
    # dkms 2.x prints "amneziawg, 1.0.0, <kernel>, ..."; 3.x prints
    # "amneziawg/1.0.0, <kernel>, ...". Both reduce to the version alone.
    versions=$(dkms status amneziawg 2>/dev/null |
        sed -E 's#^amneziawg[/,][[:space:]]*([^,]+),.*#\1#' | sort -Vu)
    count=$(echo "$versions" | grep -c '[^[:space:]]')
    [[ "$count" -gt 1 ]] || return 0

    newest=$(echo "$versions" | tail -n1)
    echo -e "${yellow}Several amneziawg versions found in the DKMS tree, keeping ${newest}:${plain}"
    while read -r v; do
        [[ -n "$v" && "$v" != "$newest" ]] || continue
        echo -e "${yellow}  removing amneziawg/${v}${plain}"
        dkms remove "amneziawg/${v}" --all &>/dev/null ||
            dkms remove -m amneziawg -v "${v}" --all &>/dev/null || true
    done <<<"$versions"
    depmod -a &>/dev/null || true
}

ensure_wireguard_native() {
    if command -v wg &>/dev/null; then
        modprobe wireguard 2>/dev/null || true
        return
    fi
    echo -e "${yellow}wireguard-tools not found, installing...${plain}"
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
    esac
    modprobe wireguard 2>/dev/null || true
}

echo -e "${green}Running...${plain}"
# Detects whether the existing install is in debug / localhost-only mode
# and inherits that setting so the update doesn't surprise the user with
# new prompts. Heuristic: panel binds to 127.0.0.1 AND no SSL cert is
# configured. Env override XUI_DEBUG_MODE=1 always wins; in that case
# XUI_DEBUG_PORT keeps whatever was either passed in env or pre-existing.
detect_debug_mode_from_existing_install() {
    if [[ "${XUI_DEBUG_MODE:-}" == "1" ]]; then
        echo -e "${yellow}Debug mode forced via XUI_DEBUG_MODE=1.${plain}"
    elif [[ -x "${xui_folder}/x-ui" ]]; then
        local existing_listen existing_cert
        existing_listen=$(${xui_folder}/x-ui setting -getListen true 2>/dev/null | grep -Eo 'listenIP: .*' | awk '{print $2}')
        existing_cert=$(${xui_folder}/x-ui setting -getCert true 2>/dev/null | grep 'cert:' | awk -F': ' '{print $2}' | tr -d '[:space:]')
        if [[ "${existing_listen}" == "127.0.0.1" && -z "${existing_cert}" ]]; then
            export XUI_DEBUG_MODE=1
            echo -e "${yellow}Detected debug / localhost-only install (listenIP=127.0.0.1, no SSL) — continuing in debug mode.${plain}"
        else
            export XUI_DEBUG_MODE=0
        fi
    else
        export XUI_DEBUG_MODE=0
    fi

    # Carry the existing port forward in debug mode so the update keeps
    # the same URL the user is already using.
    if [[ "${XUI_DEBUG_MODE}" == "1" ]]; then
        if [[ -z "${XUI_DEBUG_PORT:-}" ]]; then
            local existing_port
            existing_port=$(${xui_folder}/x-ui setting -show true 2>/dev/null | grep -Eo 'port: .+' | awk '{print $2}')
            if [[ -n "${existing_port}" && "${existing_port}" != "0" ]]; then
                export XUI_DEBUG_PORT="${existing_port}"
            else
                export XUI_DEBUG_PORT=8080
            fi
        fi
    fi
}

detect_debug_mode_from_existing_install
install_base
ensure_wireguard_native
prune_stale_amneziawg_dkms
install_nginx
update_x-ui $1
