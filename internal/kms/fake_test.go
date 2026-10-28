package kms

import (
	"fmt"
	"image"
	"maps"
	"slices"

	"github.com/stubbedev/lys-lib/drm"
	"github.com/stubbedev/lys-lib/fourcc"
	"golang.org/x/sys/unix"
)

var enums = map[string]map[string]uint64{
	"type":              {"Overlay": 0, "Primary": 1, "Cursor": 2},
	"panel orientation": {"Normal": 0, "Upside Down": 1, "Left Side Up": 2, "Right Side Up": 3},
}

type commit struct {
	flags  uint32
	user   uint64
	values map[uint32]map[string]uint64
}

type fakeConnector struct {
	conn  drm.Connector
	props map[string]uint64
}

type fakeDevice struct {
	path       string
	propIDs    map[string]uint32
	objects    map[uint32]map[string]uint64
	crtcs      []uint32
	planes     []uint32
	planeInfo  map[uint32]drm.Plane
	connectors map[uint32]*fakeConnector
	encoders   map[uint32]drm.Encoder
	blobs      map[uint32][]byte
	created    map[uint32][]byte
	fbs        map[uint32]drm.FB
	nextID     uint32
	commits    []commit
	refuse     func(flags uint32) error
	removed    []uint32
	closedFBs  []uint32
	destroyed  int
	master     bool
	masterErr  error
	closed     bool
	fd         int
}

func newFakeDevice(path string, crtcs int) *fakeDevice {
	d := &fakeDevice{
		path:       path,
		propIDs:    map[string]uint32{},
		objects:    map[uint32]map[string]uint64{},
		planeInfo:  map[uint32]drm.Plane{},
		connectors: map[uint32]*fakeConnector{},
		encoders:   map[uint32]drm.Encoder{},
		blobs:      map[uint32][]byte{},
		created:    map[uint32][]byte{},
		fbs:        map[uint32]drm.FB{},
		nextID:     1000,
		master:     true,
	}
	for i := range crtcs {
		id := uint32(10 + i)
		d.crtcs = append(d.crtcs, id)
		d.objects[id] = map[string]uint64{"ACTIVE": 0, "MODE_ID": 0}
		p := uint32(20 + i)
		d.planes = append(d.planes, p)
		d.planeInfo[p] = drm.Plane{ID: p, PossibleCRTCs: 1 << i, Formats: []fourcc.Format{fourcc.XRGB8888}}
		d.objects[p] = map[string]uint64{"type": 1, "FB_ID": 0, "CRTC_ID": 0, "SRC_X": 0, "SRC_Y": 0, "SRC_W": 0, "SRC_H": 0, "CRTC_X": 0, "CRTC_Y": 0, "CRTC_W": 0, "CRTC_H": 0, "FB_DAMAGE_CLIPS": 0}
		d.encoders[uint32(30+i)] = drm.Encoder{ID: uint32(30 + i), PossibleCRTCs: 1<<crtcs - 1}
	}
	return d
}

func (d *fakeDevice) id() uint32 {
	d.nextID++
	return d.nextID
}

func mode(w, h uint16, preferred bool) drm.Mode {
	m := drm.Mode{Clock: 148500, HDisplay: w, HSyncStart: w + 88, HSyncEnd: w + 132, HTotal: w + 280, VDisplay: h, VSyncStart: h + 4, VSyncEnd: h + 9, VTotal: h + 45, VRefresh: 60}
	if preferred {
		m.Type = drm.ModeTypePreferred
	}
	copy(m.Name[:], fmt.Sprintf("%dx%d", w, h))
	return m
}

func (d *fakeDevice) connect(id uint32, mm [2]uint32, modes ...drm.Mode) *fakeConnector {
	encs := slices.Sorted(maps.Keys(d.encoders))
	c := &fakeConnector{
		conn:  drm.Connector{ID: id, Type: 11, TypeID: id, Connection: drm.Connected, PhysicalMM: mm, Modes: modes, Encoders: encs},
		props: map[string]uint64{"CRTC_ID": 0},
	}
	d.connectors[id] = c
	d.objects[id] = c.props
	return c
}

func (d *fakeDevice) light(conn uint32, crtc uint32, m drm.Mode) {
	blob := d.id()
	d.blobs[blob] = m.Bytes()
	d.objects[crtc]["ACTIVE"] = 1
	d.objects[crtc]["MODE_ID"] = uint64(blob)
	d.objects[conn]["CRTC_ID"] = uint64(crtc)
}

func (d *fakeDevice) propID(name string) uint32 {
	id, ok := d.propIDs[name]
	if !ok {
		id = uint32(500 + len(d.propIDs))
		d.propIDs[name] = id
	}
	return id
}

func (d *fakeDevice) propName(id uint32) string {
	for name, pid := range d.propIDs {
		if pid == id {
			return name
		}
	}
	panic(fmt.Sprintf("no property %d", id))
}

func (d *fakeDevice) Path() string { return d.path }
func (d *fakeDevice) FD() int {
	if d.fd == 0 {
		fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
		if err != nil {
			panic(err)
		}
		d.fd = fd
	}
	return d.fd
}

func (d *fakeDevice) Close() error {
	d.closed = true
	if d.fd != 0 {
		return unix.Close(d.fd)
	}
	return nil
}

func (d *fakeDevice) SetClientCap(uint64, uint64) error { return nil }

func (d *fakeDevice) SetMaster() error {
	if d.masterErr != nil {
		return d.masterErr
	}
	d.master = true
	return nil
}

func (d *fakeDevice) DropMaster() error {
	if !d.master {
		return unix.EINVAL
	}
	d.master = false
	return nil
}

func (d *fakeDevice) Resources() (drm.Resources, error) {
	return drm.Resources{CRTCs: d.crtcs, Connectors: slices.Sorted(maps.Keys(d.connectors))}, nil
}

func (d *fakeDevice) Connector(id uint32) (drm.Connector, error) {
	c, ok := d.connectors[id]
	if !ok {
		return drm.Connector{}, unix.ENOENT
	}
	return c.conn, nil
}

func (d *fakeDevice) Encoder(id uint32) (drm.Encoder, error) {
	return d.encoders[id], nil
}

func (d *fakeDevice) Planes() ([]uint32, error) { return d.planes, nil }

func (d *fakeDevice) Plane(id uint32) (drm.Plane, error) { return d.planeInfo[id], nil }

func (d *fakeDevice) Properties(id, _ uint32) (map[string]drm.Property, error) {
	out := map[string]drm.Property{}
	for name, v := range d.objects[id] {
		out[name] = drm.Property{ID: d.propID(name), Value: v}
	}
	return out, nil
}

func (d *fakeDevice) PropertyInfo(id uint32) (drm.PropertyInfo, error) {
	name := d.propName(id)
	return drm.PropertyInfo{Name: name, Enums: enums[name]}, nil
}

func (d *fakeDevice) Blob(id uint32) ([]byte, error) {
	b, ok := d.blobs[id]
	if !ok {
		return nil, unix.ENOENT
	}
	return b, nil
}

func (d *fakeDevice) CreateBlob(data []byte) (uint32, error) {
	id := d.id()
	d.blobs[id] = slices.Clone(data)
	d.created[id] = d.blobs[id]
	return id, nil
}

func (d *fakeDevice) DestroyBlob(id uint32) error {
	if _, ok := d.blobs[id]; !ok {
		return unix.ENOENT
	}
	delete(d.blobs, id)
	return nil
}

func (d *fakeDevice) CreateDumb(size image.Point) (drm.Dumb, error) {
	return drm.Dumb{Handle: d.id(), Pitch: uint32(size.X * 4), Pix: make([]byte, size.X*size.Y*4)}, nil
}

func (d *fakeDevice) DestroyDumb(drm.Dumb) error {
	d.destroyed++
	return nil
}

func (d *fakeDevice) AddFB2(fb drm.FB) (uint32, error) {
	id := d.id()
	d.fbs[id] = fb
	return id, nil
}

func (d *fakeDevice) RemoveFB(id uint32) error {
	delete(d.fbs, id)
	d.removed = append(d.removed, id)
	return nil
}

func (d *fakeDevice) CloseFB(id uint32) error {
	delete(d.fbs, id)
	d.closedFBs = append(d.closedFBs, id)
	return nil
}

func (d *fakeDevice) Commit(r *drm.Request, flags uint32, user uint64) error {
	if d.refuse != nil {
		if err := d.refuse(flags); err != nil {
			return err
		}
	}
	values := map[uint32]map[string]uint64{}
	r.Each(func(obj, prop uint32, v uint64) {
		if values[obj] == nil {
			values[obj] = map[string]uint64{}
		}
		values[obj][d.propName(prop)] = v
	})
	d.commits = append(d.commits, commit{flags: flags, user: user, values: values})
	if flags&drm.AtomicTestOnly != 0 {
		return nil
	}
	for obj, vs := range values {
		maps.Copy(d.objects[obj], vs)
	}
	return nil
}

func (d *fakeDevice) real() []commit {
	return slices.DeleteFunc(slices.Clone(d.commits), func(c commit) bool { return c.flags&drm.AtomicTestOnly != 0 })
}
