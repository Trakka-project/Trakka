# Toolchain for building Trakka's Android app (a Capacitor shell, android/native): used by
# `make build-apk-capacitor` and `make apk-keystore`; see docs/MOBILE_BUILD.md.
#
# The image only holds tools. android/ itself (sources, settings, signing key) is mounted when
# the container runs (see the Makefile), so nothing private ever enters the image, and it only
# needs rebuilding when a pinned version below changes.
#
# Building this image downloads the Android SDK and accepts the Android Software Development Kit
# License Agreement on your behalf (https://developer.android.com/studio/terms), which is what
# `sdkmanager --licenses` would otherwise ask you to do interactively.

FROM node:24.21.0-trixie-slim

# What android/native needs, to recheck whenever Capacitor or the Android Gradle Plugin is
# upgraded: compileSdk (android/native/variables.gradle); the build-tools the Android Gradle
# Plugin of android/native/build.gradle uses by default, the project setting none, and the
# platform-tools it also expects; and JDK 21, which Capacitor 8 compiles with
# (node_modules/@capacitor/android/capacitor/build.gradle). The SDK stays read-only for the
# build, so anything missing here fails the build instead of being downloaded on the fly.
ARG ANDROID_PLATFORM=android-36
ARG ANDROID_BUILD_TOOLS=35.0.0
# Android command-line tools 21.0, only used here to install the packages above. Deliberately not
# newer: 22.0 adds Google's "Android CLI", which the sdkmanager of 23.0 delegates to, and which
# downloads a runtime of its own and uploads usage metrics by default; 21.0 has the classic
# sdkmanager. Google's repository index
# (https://dl.google.com/android/repository/repository2-3.xml) only lists a SHA-1 for it
# (63523a02a975a81102238566f2a16c057d52301e); this is the SHA-256 of the archive that matched it.
ARG CMDLINE_TOOLS_BUILD=15641748
ARG CMDLINE_TOOLS_SHA256=a66d5ef0238fc0162e9c1446602ce0dd41702d4dd7a94d2ce42d12b7f80baf7e

ENV JAVA_HOME=/opt/jdk \
    ANDROID_HOME=/opt/android-sdk
ENV PATH=$JAVA_HOME/bin:$ANDROID_HOME/cmdline-tools/latest/bin:$PATH

# Slim images ship without /usr/share/man/man1, and the JDK's update-alternatives step fails
# without it. JAVA_HOME is a stable link to the architecture-specific JDK directory.
RUN mkdir -p /usr/share/man/man1 \
 && apt-get update \
 && apt-get install -y --no-install-recommends openjdk-21-jdk-headless unzip curl ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && ln -s "$(dirname "$(dirname "$(readlink -f "$(command -v javac)")")")" "$JAVA_HOME"

RUN curl -fsSLo /tmp/cmdline-tools.zip \
      "https://dl.google.com/android/repository/commandlinetools-linux-${CMDLINE_TOOLS_BUILD}_latest.zip" \
 && echo "${CMDLINE_TOOLS_SHA256}  /tmp/cmdline-tools.zip" | sha256sum -c - \
 && mkdir -p "$ANDROID_HOME/cmdline-tools" \
 && unzip -q /tmp/cmdline-tools.zip -d /tmp \
 && mv /tmp/cmdline-tools "$ANDROID_HOME/cmdline-tools/latest" \
 && rm /tmp/cmdline-tools.zip \
 && yes | sdkmanager --sdk_root="$ANDROID_HOME" "platforms;${ANDROID_PLATFORM}" "build-tools;${ANDROID_BUILD_TOOLS}" platform-tools >/dev/null

# The build runs as your own UID (see the Makefile), which has no home directory in this image:
# give it a writable one. Gradle's and npm's downloads go to /cache, which the Makefile mounts
# from the host so that they survive between builds.
RUN mkdir -m 1777 /tmp/home /cache /cache/gradle /cache/npm
ENV HOME=/tmp/home \
    ANDROID_USER_HOME=/tmp/home/.android \
    GRADLE_USER_HOME=/cache/gradle \
    npm_config_cache=/cache/npm \
    npm_config_update_notifier=false

WORKDIR /work/android
ENTRYPOINT ["node", "build.mjs"]
CMD ["build"]
