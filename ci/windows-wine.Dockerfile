# Wine for running windows/arm64 binaries on an arm64 Linux host (ci/windows-wine.sh),
# built from source. Debian's packaged Wine (10.0) hangs in wineboot on CPUs with
# pointer authentication: its arm64 unix-side DWARF unwinder ignores
# DW_CFA_AARCH64_negate_ra_state and follows signed return addresses. Wine 11 no longer
# unwinds unix code that way on arm64. The PE side is compiled with Debian's clang/lld,
# for aarch64 and x86_64: the installed headers and both architectures' import libraries
# are also the SDK the Windows glue DLLs are built against (ci/windows-wine.sh sdk). Only
# arm64 binaries run here; x86-64 ones would need an emulator.
ARG BASE=debian@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

FROM ${BASE} AS build
# 11.21, not 11.22: 11.22's 5484caec5e zeroes v16-v31 of a thread whose context is set
# without CONTEXT_FLOATING_POINT, which is how Go's async preemption sets it (journal
# 2026-11-19). Move past 11.22 only to a release that fixes it.
ARG WINE_VERSION=11.21
ARG WINE_SHA256=5a40f593432d81fd3a1646db503f21ce42b8894d6d80c34c3a48b886edc54e3d
RUN printf 'deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/20261108T000000Z trixie main\n' > /etc/apt/sources.list \
 && rm -f /etc/apt/sources.list.d/debian.sources \
 && apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      gcc libc6-dev make flex bison clang lld llvm xz-utils curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL -o /wine.tar.xz https://dl.winehq.org/wine/source/11.x/wine-${WINE_VERSION}.tar.xz \
 && echo "${WINE_SHA256}  /wine.tar.xz" | sha256sum -c - \
 && mkdir /src && tar -xJf /wine.tar.xz -C /src && cd /src/wine-${WINE_VERSION} \
 && ./configure --prefix=/opt/wine --enable-archs=aarch64,x86_64 --without-x --without-freetype \
      --without-gnutls --without-gstreamer --without-pulse --without-alsa --without-cups \
      --without-dbus --without-sane --without-usb --without-v4l2 --without-wayland \
      --without-vulkan --without-opencl --without-pcap --without-krb5 --without-netapi \
      --without-sdl --without-udev --without-unwind --without-capi --without-gphoto \
      --without-fontconfig --without-inotify --without-osmesa --without-oss --without-opengl \
      --without-xinerama --without-xrandr --without-xshape --without-xshm --without-xxf86vm \
      --without-xcomposite --without-xcursor --without-xfixes --without-xinput --without-xinput2 \
      --without-ffmpeg --without-gssapi \
 && make -j"$(nproc)" && make install && rm -rf /src /wine.tar.xz

FROM ${BASE}
COPY --from=build /opt/wine /opt/wine
ENV PATH=/opt/wine/bin:$PATH WINEDEBUG=-all WINEPREFIX=/wineprefix WINEDLLOVERRIDES="mscoree=d;mshtml=d"
RUN wine wineboot --init && wineserver -w
