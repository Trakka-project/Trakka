#!/bin/sh
# Entrypoint of the builder image (android/Dockerfile): runs build.mjs as whichever UID the
# Makefile passes. Docker's `--user UID:GID` doesn't add that UID to the image's /etc/passwd
# (Podman's `--userns=keep-id` does), and the Capacitor CLI dies without an entry for it
# (os.userInfo(): "uv_os_get_passwd returned ENOENT"). That is the case on GitHub's runners
# (UID 1001), and on any host where you aren't UID 1000, the image's `node` user. nss_wrapper
# gives the missing entry to every process started from here, without making /etc/passwd
# writable.
set -eu

uid="$(id -u)"
gid="$(id -g)"
if ! getent passwd "$uid" >/dev/null 2>&1; then
  export NSS_WRAPPER_PASSWD="$HOME/.nss_passwd" NSS_WRAPPER_GROUP="$HOME/.nss_group"
  { cat /etc/passwd; printf 'trakka:x:%s:%s:Trakka build:%s:/bin/sh\n' "$uid" "$gid" "$HOME"; } \
    > "$NSS_WRAPPER_PASSWD"
  cp /etc/group "$NSS_WRAPPER_GROUP"
  getent group "$gid" >/dev/null 2>&1 || printf 'trakka:x:%s:\n' "$gid" >> "$NSS_WRAPPER_GROUP"
  export LD_PRELOAD="libnss_wrapper.so${LD_PRELOAD:+ $LD_PRELOAD}"
fi

exec node /work/android/build.mjs "$@"
