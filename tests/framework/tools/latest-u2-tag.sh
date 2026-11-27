#!/usr/bin/env bash
# Print the newest update-level (U2) image tag of a Gateway release, for example 1.2.0.37 for
# release 1.2.0. Only a tag that every Gateway image carries in the WSO2 registry qualifies: the
# controller and runtime the suite starts, and the gateway-builder it uses to build policy images.
#
# Usage:  WSO2_REGISTRY_USERNAME=... WSO2_REGISTRY_PASSWORD=... latest-u2-tag.sh <release>
# Optional: WSO2_REGISTRY_HOST (default registry.wso2.com), WSO2_REGISTRY_NAMESPACE (default
# wso2-api-platform). The tag is the only thing written to stdout.
set -euo pipefail

registry="${WSO2_REGISTRY_HOST:-registry.wso2.com}"
namespace="${WSO2_REGISTRY_NAMESPACE:-wso2-api-platform}"
images=(gateway-controller gateway-runtime gateway-builder)

die() { echo "latest-u2-tag: $*" >&2; exit 1; }

release="${1:-}"
[[ "$release" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: latest-u2-tag.sh <release, for example 1.2.0>"
[ -n "${WSO2_REGISTRY_USERNAME:-}" ] || die "WSO2_REGISTRY_USERNAME is not set"
[ -n "${WSO2_REGISTRY_PASSWORD:-}" ] || die "WSO2_REGISTRY_PASSWORD is not set"
command -v jq >/dev/null || die "jq is required"

# The credentials are handed to curl on stdin, so they never appear in its arguments.
curl_authenticated() {
	local user="${WSO2_REGISTRY_USERNAME//\\/\\\\}" secret="${WSO2_REGISTRY_PASSWORD//\\/\\\\}"
	user="${user//\"/\\\"}"
	secret="${secret//\"/\\\"}"
	printf 'user = "%s:%s"\n' "$user" "$secret" | curl --fail --silent --show-error --max-time 60 --config - "$@"
}

# Print every tag of one image repository, following the registry's pagination.
list_tags() {
	local repository="$1" url challenge realm service token headers body next
	url="https://${registry}/v2/${repository}/tags/list?n=1000"

	challenge="$(curl --silent --max-time 60 --dump-header - --output /dev/null "$url" | tr -d '\r' |
		sed -n 's/^[Ww][Ww][Ww]-[Aa]uthenticate: *//p')"
	realm="$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"$challenge")"
	service="$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"$challenge")"
	[ -n "$realm" ] || die "the registry did not offer a token endpoint for ${repository}"
	# The credentials go only to the registry's own host.
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
