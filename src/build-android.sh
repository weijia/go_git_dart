#!/bin/bash

set -eux

# Assumes Android Studio is installed on the standard path along with Android SDK/NDK.
# NDK r28+ emits 16 KB-aligned ELF LOAD segments by default; keep explicit
# linker flags so older compatible toolchains do the same.
export ANDROID_HOME=${ANDROID_HOME:-$HOME/Library/Android/sdk}
if [ -z "${ANDROID_NDK_HOME:-}" ]; then
    NDK_VERSION=$(find "$ANDROID_HOME/ndk" -maxdepth 1 -mindepth 1 -type d -exec basename {} \; | sort -t. -k1,1n -k2,2n -k3,3n | tail -1)
    export ANDROID_NDK_HOME=$ANDROID_HOME/ndk/$NDK_VERSION
else
    export ANDROID_NDK_HOME
fi
export JAVA_HOME=${JAVA_HOME:-/Applications/Android\ Studio.app/Contents/jre/Contents/Home}

ROOTDIR=$(cd $(dirname $0); pwd -P)
DISTDIR=$ROOTDIR/../android/src/main
TMPDIR=$ROOTDIR/tmp/android

HOST_TAG=$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m)
if [ "$HOST_TAG" = "darwin-arm64" ]; then
    HOST_TAG=darwin-x86_64
fi

export PATH=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$HOST_TAG/bin:$ANDROID_HOME/platform-tools:$PATH

rm -rf $DISTDIR/jniLibs $TMPDIR
mkdir -p \
    $DISTDIR \
    $TMPDIR/armeabi-v7a \
    $TMPDIR/arm64-v8a \
    $TMPDIR/x86 \
    $TMPDIR/x86_64

ANDROID_SDK_VERSION=34
CLANG_SUFFIX=-linux-android$ANDROID_SDK_VERSION-clang

export GOOS=android
export CGO_ENABLED=1
GO_LDFLAGS='-extldflags=-Wl,-z,max-page-size=16384,-z,common-page-size=16384'

GOARCH=arm CC=armv7a-linux-androideabi$ANDROID_SDK_VERSION-clang CXX=armv7a-linux-androideabi$ANDROID_SDK_VERSION-clang++ \
    go build -v -x -buildmode=c-shared -trimpath -ldflags="$GO_LDFLAGS" -o=$TMPDIR/armeabi-v7a/go_git_dart.so
GOARCH=arm64 CC=aarch64$CLANG_SUFFIX CXX=aarch64$CLANG_SUFFIX++ \
    go build -v -x -buildmode=c-shared -trimpath -ldflags="$GO_LDFLAGS" -o=$TMPDIR/arm64-v8a/go_git_dart.so
GOARCH=386 CC=i686$CLANG_SUFFIX CXX=i686$CLANG_SUFFIX++ \
    go build -v -x -buildmode=c-shared -trimpath -ldflags="$GO_LDFLAGS" -o=$TMPDIR/x86/go_git_dart.so
GOARCH=amd64 CC=x86_64$CLANG_SUFFIX CXX=x86_64$CLANG_SUFFIX++ \
    go build -v -x -buildmode=c-shared -trimpath -ldflags="$GO_LDFLAGS" -o=$TMPDIR/x86_64/go_git_dart.so

cp -rf $TMPDIR $DISTDIR/jniLibs
find $DISTDIR -name "*.h" -exec rm {} \;

echo "Android build successful"
