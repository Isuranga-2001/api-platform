#!/usr/bin/env bash
# Prints the newest update-level (U2) tag of a Gateway release (such as 1.2.0.5 for 1.2.0) that
# the controller, runtime and builder images all carry in the registry.
#
# Usage: HARBOR_REGISTRY_USERNAME=... HARBOR_REGISTRY_PASSWORD=... latest-u2-tag.sh <release>
# Optional: HARBOR_REGISTRY_HOST (registry.wso2.com), HARBOR_REGISTRY_NAMESPACE (wso2-api-platform).
set -euo pipefail

registry="${HARBOR_REGISTRY_HOST:-registry.wso2.com}"
namespace="${HARBOR_REGISTRY_NAMESPACE:-wso2-api-platform}"
images=(gateway-controller gateway-runtime gateway-builder)

die() { echo "latest-u2-tag: $*" >&2; exit 1; }

release="${1:-}"
[[ "$release" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: latest-u2-tag.sh <release, for example 1.2.0>"
[ -n "${HARBOR_REGISTRY_USERNAME:-}" ] || die "HARBOR_REGISTRY_USERNAME is not set"
[ -n "${HARBOR_REGISTRY_PASSWORD:-}" ] || die "HARBOR_REGISTRY_PASSWORD is not set"
command -v jq >/dev/null || die "jq is required"

# Credentials go to curl on stdin, not in its arguments.
curl_authenticated() {
	local user="${HARBOR_REGISTRY_USERNAME//\\/\\\\}" secret="${HARBOR_REGISTRY_PASSWORD//\\/\\\\}"
	user="${user//\"/\\\"}"
	secret="${secret//\"/\\\"}"
	printf 'user = "%s:%s"\n' "$user" "$secret" | curl --fail --silent --show-error --max-time 60 --config - "$@"
}

# Prints every tag of a repository, following pagination.
list_tags() {
	local repository="$1" url challenge realm service token headers body next
	url="https://${registry}/v2/${repository}/tags/list?n=1000"

	challenge="$(curl --silent --max-time 60 --dump-header - --output /dev/null "$url" | tr -d '\r' |
		sed -n 's/^[Ww][Ww][Ww]-[Aa]uthenticate: *//p')"
	realm="$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"$challenge")"
	service="$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"$challenge")"
	[ -n "$realm" ] || die "the registry did not offer a token endpoint for ${repository}"
	# Send credentials only to the registry's own host.
	case "$realm" in
		"https://${registry}/"*) ;;
		*) die "the registry's token endpoint is not on ${registry}" ;;
	esac

	token="$(curl_authenticated --get --data-urlencode "service=${service}" \
		--data-urlencode "scope=repository:${repository}:pull" "$realm" | jq -r '.token // empty')" ||
		die "the registry refused the credentials for ${repository}"
	[ -n "$token" ] || die "the registry returned no token for ${repository}"

	headers="$(mktemp)"
	while [ -n "$url" ]; do
		body="$(curl --fail --silent --show-error --max-time 60 --dump-header "$headers" \
			--header "Authorization: Bearer ${token}" "$url")" || {
			rm -f "$headers"
			die "could not list the tags of ${repository}"
		}
		jq -r '.tags[]?' <<<"$body"
		next="$(tr -d '\r' <"$headers" | sed -n 's/^[Ll]ink: *<\([^>]*\)>; *rel="next".*/\1/p')"
		case "$next" in
			"") url="" ;;
			/*) url="https://${registry}${next}" ;;
			*) url="$next" ;;
		esac
	done
	rm -f "$headers"
}

pattern="^${release//./\\.}\\.[0-9]+\$"
common=""
for image in "${images[@]}"; do
	all_tags="$(list_tags "${namespace}/${image}")" || exit 1
	tags="$(grep -E "$pattern" <<<"$all_tags" | sort -u || true)"
	[ -n "$tags" ] || die "${namespace}/${image} has no ${release}.N tag"
	if [ -z "$common" ]; then
		common="$tags"
	else
		common="$(comm -12 <(printf '%s\n' "$common") <(printf '%s\n' "$tags"))"
	fi
done
[ -n "$common" ] || die "no ${release}.N tag exists on every Gateway image"

printf '%s\n' "$common" | sort -t. -k4,4n | tail -n 1
