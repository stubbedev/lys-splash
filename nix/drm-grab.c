#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <unistd.h>
#include <drm_fourcc.h>
#include <xf86drm.h>
#include <xf86drmMode.h>

static void die(const char *what, const char *detail) {
  fprintf(stderr, "drm-grab: %s %s: %s\n", what, detail, strerror(errno));
  exit(1);
}

static uint32_t crtc_of(int fd, drmModeConnector *conn) {
  if (conn->encoder_id == 0)
    return 0;
  drmModeEncoder *enc = drmModeGetEncoder(fd, conn->encoder_id);
  if (!enc)
    die("get encoder of", drmModeGetConnectorTypeName(conn->connector_type));
  uint32_t crtc = enc->crtc_id;
  drmModeFreeEncoder(enc);
  return crtc;
}

static uint32_t fb_on(int fd, uint32_t crtc) {
  drmModePlaneRes *planes = drmModeGetPlaneResources(fd);
  if (!planes)
    die("list planes", "");
  uint32_t fb = 0;
  for (uint32_t i = 0; i < planes->count_planes && fb == 0; i++) {
    drmModePlane *p = drmModeGetPlane(fd, planes->planes[i]);
    if (!p)
      die("get plane", "");
    if (p->crtc_id == crtc)
      fb = p->fb_id;
    drmModeFreePlane(p);
  }
  drmModeFreePlaneResources(planes);
  return fb;
}

static void write_ppm(int fd, uint32_t fb_id, const char *path) {
  drmModeFB2 *fb = drmModeGetFB2(fd, fb_id);
  if (!fb)
    die("get framebuffer for", path);
  if (fb->pixel_format != DRM_FORMAT_XRGB8888 && fb->pixel_format != DRM_FORMAT_ARGB8888) {
    fprintf(stderr, "drm-grab: %s: framebuffer %u is not 32-bit RGB\n", path, fb_id);
    exit(1);
  }
  if (fb->handles[0] == 0) {
    fprintf(stderr, "drm-grab: %s: the kernel withheld the handle of framebuffer %u\n", path, fb_id);
    exit(1);
  }
  struct drm_mode_map_dumb map = {.handle = fb->handles[0]};
  if (drmIoctl(fd, DRM_IOCTL_MODE_MAP_DUMB, &map))
    die("map the buffer for", path);
  size_t len = (size_t)fb->pitches[0] * fb->height;
  uint8_t *pix = mmap(NULL, len, PROT_READ, MAP_SHARED, fd, (off_t)map.offset);
  if (pix == MAP_FAILED)
    die("mmap the buffer for", path);
  FILE *out = fopen(path, "wb");
  if (!out)
    die("create", path);
  fprintf(out, "P6\n%u %u\n255\n", fb->width, fb->height);
  for (uint32_t y = 0; y < fb->height; y++) {
    const uint8_t *row = pix + (size_t)y * fb->pitches[0];
    for (uint32_t x = 0; x < fb->width; x++) {
      const uint8_t rgb[3] = {row[x * 4 + 2], row[x * 4 + 1], row[x * 4]};
      fwrite(rgb, 1, 3, out);
    }
  }
  if (fclose(out))
    die("write", path);
  munmap(pix, len);
  drmCloseBufferHandle(fd, fb->handles[0]);
  drmModeFreeFB2(fb);
}

int main(int argc, char **argv) {
  if (argc != 3) {
    fprintf(stderr, "usage: drm-grab CARD DIR\n"
                    "writes DIR/CONNECTOR.ppm for every connector that shows a framebuffer\n");
    return 2;
  }
  int fd = open(argv[1], O_RDWR | O_CLOEXEC);
  if (fd < 0)
    die("open", argv[1]);
  if (drmSetClientCap(fd, DRM_CLIENT_CAP_UNIVERSAL_PLANES, 1))
    die("enable universal planes on", argv[1]);
  drmModeRes *res = drmModeGetResources(fd);
  if (!res)
    die("get the resources of", argv[1]);
  int grabbed = 0;
  for (int i = 0; i < res->count_connectors; i++) {
    drmModeConnector *conn = drmModeGetConnector(fd, res->connectors[i]);
    if (!conn)
      die("get a connector of", argv[1]);
    uint32_t crtc = crtc_of(fd, conn);
    uint32_t fb = crtc ? fb_on(fd, crtc) : 0;
    if (fb) {
      char path[4096];
      snprintf(path, sizeof path, "%s/%s-%u.ppm", argv[2],
               drmModeGetConnectorTypeName(conn->connector_type), conn->connector_type_id);
      write_ppm(fd, fb, path);
      printf("%s\n", path);
      grabbed++;
    }
    drmModeFreeConnector(conn);
  }
  drmModeFreeResources(res);
  close(fd);
  if (grabbed == 0) {
    fprintf(stderr, "drm-grab: no connector of %s shows a framebuffer\n", argv[1]);
    return 1;
  }
  return 0;
}
