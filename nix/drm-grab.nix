{
  runCommandCC,
  pkg-config,
  libdrm,
}:
runCommandCC "drm-grab" {
  nativeBuildInputs = [pkg-config];
  buildInputs = [libdrm];
} "mkdir -p $out/bin && $CC -std=gnu11 -O2 -Wall -Wextra -Werror -o $out/bin/drm-grab ${./drm-grab.c} $(pkg-config --cflags --libs libdrm)"
