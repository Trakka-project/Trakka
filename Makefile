# Android app (APK): a Capacitor shell that opens the PWA of any Trakka server, see
# docs/MOBILE_BUILD.md. The Go server needs no Makefile (CLAUDE.md lists its commands); these
# targets exist because the APK needs Node.js, a JDK and the Android SDK, which they run from a
# container image (android/Dockerfile) so that nothing beyond Podman or Docker has to be installed.
#
#   make apk-keystore          create the signing key (once)
#   make build-apk-capacitor   build android/out/trakka.apk

# Podman when installed (rootless, no daemon), Docker otherwise.
CONTAINER_ENGINE ?= $(if $(shell command -v podman 2>/dev/null),podman,docker)
APK_BUILDER_IMAGE ?= localhost/trakka-apk-builder:capacitor-8
# Gradle's and npm's downloads, kept between builds.
APK_CACHE ?= $(HOME)/.cache/trakka-apk
# Extra flags for `run` (e.g. --network=host when containers cannot resolve DNS, see
# docs/DEPLOYMENT.md) and for building the image.
APK_RUN_FLAGS ?=
APK_IMAGE_BUILD_FLAGS ?=

# Run as yourself, so that everything written under android/ stays yours: Podman maps your user
# into the container, rootful Docker gets your UID. With rootless Docker, pass APK_USER_FLAGS=
# (the container's root is you).
ifneq ($(findstring podman,$(shell $(CONTAINER_ENGINE) --version 2>/dev/null)),)
APK_USER_FLAGS ?= --userns=keep-id
else
APK_USER_FLAGS ?= --user $(shell id -u):$(shell id -g)
endif

# Settings passed through to android/build.mjs when set (android/apk.env.example).
APK_SETTINGS := ANDROID_VERSION_CODE ANDROID_VERSION_NAME ANDROID_KEYSTORE ANDROID_KEY_ALIAS \
	ANDROID_KEYSTORE_PASSWORD ANDROID_KEY_PASSWORD

# Only android/ is mounted: the rest of the repository (a local .env, trakka.db) stays out of a
# container that runs Gradle plugins and npm packages.
APK_RUN = mkdir -p "$(APK_CACHE)/gradle" "$(APK_CACHE)/npm" && \
	$(CONTAINER_ENGINE) run --rm $(APK_USER_FLAGS) \
	$(foreach setting,$(APK_SETTINGS),-e $(setting)) \
	-v "$(CURDIR)/android:/work/android:z" \
	-v "$(APK_CACHE)/gradle:/cache/gradle:z" \
	-v "$(APK_CACHE)/npm:/cache/npm:z" \
	$(APK_RUN_FLAGS) $(APK_BUILDER_IMAGE)

.DEFAULT_GOAL := help
.PHONY: help apk-builder-image apk-keystore build-apk-capacitor build-apk apk-clean

help:
	@echo "Android app (docs/MOBILE_BUILD.md):"
	@echo "  make apk-keystore          create the APK signing key (once)"
	@echo "  make build-apk-capacitor   build android/out/trakka.apk (alias: make build-apk)"
	@echo "  make apk-clean             delete build outputs (keeps the signing key)"

apk-builder-image:
	$(CONTAINER_ENGINE) build $(APK_IMAGE_BUILD_FLAGS) -t $(APK_BUILDER_IMAGE) -f android/Dockerfile android

apk-keystore: apk-builder-image
	$(APK_RUN) keystore

build-apk-capacitor: apk-builder-image
	$(APK_RUN) build

build-apk: build-apk-capacitor

apk-clean:
	rm -rf android/out android/native/app/build android/native/build android/native/.gradle
