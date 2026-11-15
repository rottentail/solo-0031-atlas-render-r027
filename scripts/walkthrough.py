#!/usr/bin/env python3
"""Run bounded production flows against a temporary Atlas Render server."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import zlib

ROOT = Path(__file__).resolve().parents[1]


def sample():
    return {"width": 320, "height": 240, "background": "#F5F1E8", "layers": [
        {"id": "plate", "kind": "rectangle", "width": 220, "height": 150,
         "transform": {"x": 45, "y": 35, "scaleX": 1, "scaleY": 1, "degrees": 5},
         "opacity": 1, "fill": {"kind": "linear", "color": "#145A70", "endColor": "#32A5A0", "from": {"x": 0, "y": 0}, "to": {"x": 220, "y": 150}}},
        {"id": "accent", "kind": "ellipse", "width": 130, "height": 130,
         "transform": {"x": 150, "y": 95, "scaleX": 1, "scaleY": 1, "degrees": 0},
         "opacity": .85, "fill": {"kind": "solid", "color": "#E87E4F"}},
        {"id": "inset", "kind": "group", "transform": {"x": 150, "y": 100, "scaleX": 1, "scaleY": 1, "degrees": -8}, "opacity": .9,
         "clip": {"x": 0, "y": 0, "width": 90, "height": 75}, "children": [
             {"id": "facet", "kind": "polygon", "width": 100, "height": 100,
              "transform": {"scaleX": 1, "scaleY": 1}, "opacity": .8,
              "fill": {"kind": "solid", "color": "#FFF4D5"}, "points": [{"x": 10, "y": 90}, {"x": 50, "y": 0}, {"x": 100, "y": 90}]}]}]}


def tiny_png():
    def chunk(tag, data):
        return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2, 2, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(b'\0' + bytes([240, 40, 80, 255] * 2) + b'\0' + bytes([40, 80, 240, 128] * 2))) + chunk(b'IEND', b'')


def decode_png(data):
    """Decode an 8-bit RGBA PNG into (width, height, rows of (r, g, b, a))."""
    assert data[:8] == b'\x89PNG\r\n\x1a\n'
    pos, width, height, bit, colour = 8, 0, 0, 0, 0
    idat = b''
    while pos < len(data):
        (size,) = struct.unpack('>I', data[pos:pos + 4])
        kind = data[pos + 4:pos + 8]
        body = data[pos + 8:pos + 8 + size]
        if kind == b'IHDR':
            width, height, bit, colour, _, _, _ = struct.unpack('>IIBBBBB', body)
        elif kind == b'IDAT':
            idat += body
        pos += 12 + size
    assert bit == 8 and colour in (2, 6)
    channels = 4 if colour == 6 else 3
    raw = zlib.decompress(idat)
    stride = width * channels
    rows, prev, offset = [], bytearray(stride), 0
    for _ in range(height):
        filt = raw[offset]
        line = bytearray(raw[offset + 1:offset + 1 + stride])
        offset += 1 + stride
        for i in range(stride):
            left = line[i - channels] if i >= channels else 0
            up = prev[i]
            up_left = prev[i - channels] if i >= channels else 0
            if filt == 1:
                line[i] = (line[i] + left) & 255
            elif filt == 2:
                line[i] = (line[i] + up) & 255
            elif filt == 3:
                line[i] = (line[i] + ((left + up) // 2)) & 255
            elif filt == 4:
                p = left + up - up_left
                pa, pb, pc = abs(p - left), abs(p - up), abs(p - up_left)
                guess = left if pa <= pb and pa <= pc else up if pb <= pc else up_left
                line[i] = (line[i] + guess) & 255
        expanded = None
        if channels == 3:
            expanded = bytearray(width * 4)
            for i in range(width):
                expanded[i * 4:i * 4 + 3] = line[i * 3:i * 3 + 3]
                expanded[i * 4 + 3] = 255
        rows.append([tuple((expanded if expanded is not None else line)[i:i + 4]) for i in range(0, width * 4, 4)])
        # Keep the previous scanline in the encoded channel layout so filter
        # references use the correct byte stride on the next row.
        prev = line
    return width, height, rows


def gradient_stops_doc():
    # Rectangle fades from opaque red to a fully transparent (arbitrarily black)
    # stop over a black base layer; premultiplied interpolation must keep the
    # visible color red instead of dragging the unseen black into a dark fringe.
    black_base = {"id": "solid-tail", "kind": "rectangle", "width": 100, "height": 20,
                  "transform": {"x": 10, "y": 10, "scaleX": 1, "scaleY": 1, "degrees": 0},
                  "opacity": 1, "fill": {"kind": "solid", "color": "#000000"}}
    fade = {"id": "fade", "kind": "rectangle", "width": 100, "height": 20,
            "transform": {"x": 10, "y": 10, "scaleX": 1, "scaleY": 1, "degrees": 0},
            "opacity": 1,
            "fill": {"kind": "linear", "from": {"x": 0, "y": 0}, "to": {"x": 100, "y": 0},
                     "interpolation": "linear-rgb",
                     "stops": [
                         {"position": 0, "color": "#FF0000", "alpha": 1},
                         {"position": 0.5, "color": "#FF8800", "alpha": 0.6},
                         {"position": 0.5, "color": "#00AAFF", "alpha": 0.6},
                         {"position": 1, "color": "#000000", "alpha": 0}]}}
    return {"width": 120, "height": 40, "background": "#F5F1E8", "layers": [black_base, fade]}


class Demo:
    def __init__(self, binary, directory):
        self.binary = binary
        self.directory = directory
        self.proc = None
        self.log = open(directory / 'server.log', 'ab')
        self.start()

    def start(self):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.url = f'http://127.0.0.1:{port}'
        env = dict(os.environ, ATLAS_ADDR=f'127.0.0.1:{port}', ATLAS_STATE=str(self.directory / 'state.json'))
        self.proc = subprocess.Popen([str(self.binary)], env=env, stdout=self.log, stderr=self.log)
        for _ in range(100):
            try:
                self.call('GET', '/healthz')
                return
            except (urllib.error.URLError, ConnectionError):
                if self.proc.poll() is not None:
                    raise RuntimeError((self.directory / 'server.log').read_text())
                time.sleep(.03)
        raise RuntimeError('server startup deadline exceeded')

    def stop(self, abrupt=False):
        if self.proc and self.proc.poll() is None:
            (self.proc.kill if abrupt else self.proc.terminate)()
            self.proc.wait(timeout=15)

    def call(self, method, path, body=None, status=200, headers=None):
        hdr = dict(headers or {})
        if isinstance(body, (dict, list)):
            body = json.dumps(body).encode()
            hdr['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.url + path, data=body, method=method, headers=hdr)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: expected {status}, received {response.status}: {data[:500]!r}')
            if response.headers.get('Content-Type', '').startswith('application/json'):
                return json.loads(data)
            return data

    def wait(self, job_id, wanted=('succeeded',)):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            job = self.call('GET', '/v1/jobs/' + job_id)
            if job['state'] in wanted:
                return job
            if job['state'] in ('failed', 'cancelled'):
                raise RuntimeError(f'unexpected job outcome: {job}')
            time.sleep(.025)
        raise RuntimeError('render deadline exceeded')

    def submit(self, snapshot, key='render-one', scale=1):
        return self.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': scale}, 202, {'Idempotency-Key': key})


def run_gradient_stops(demo, out):
    doc = gradient_stops_doc()
    saved = demo.call('PUT', '/v1/scenes/gradient', {'name': '多停靠淡出', 'expectedRevision': 0, 'document': doc})
    fill = saved['document']['layers'][1]['fill']
    assert fill['stops'] == doc['layers'][1]['fill']['stops']
    assert fill['interpolation'] == 'linear-rgb'

    def reject(change, label):
        bad = json.loads(json.dumps(doc))
        change(bad)
        demo.call('PUT', '/v1/scenes/' + label, {'name': label, 'expectedRevision': 0, 'document': bad}, 400)

    g = lambda b: b['layers'][1]['fill']
    reject(lambda b: g(b).__setitem__('stops', g(b)['stops'][:1]), 'one-stop')
    reject(lambda b: g(b)['stops'].__setitem__(0, {**g(b)['stops'][0], 'position': 1.5}), 'bad-position')
    reject(lambda b: g(b)['stops'].__setitem__(0, {**g(b)['stops'][0], 'alpha': -0.1}), 'bad-alpha')
    reject(lambda b: g(b)['stops'].__setitem__(0, {**g(b)['stops'][0], 'color': 'red'}), 'bad-color')
    reject(lambda b: g(b).__setitem__('interpolation', 'hsv'), 'bad-interpolation')
    reject(lambda b: g(b).__setitem__('endColor', '#000000'), 'stops-with-endcolor')
    reject(lambda b: b['layers'][0]['fill'].__setitem__('stops', [{'position': 0, 'color': '#FF0000', 'alpha': 1}, {'position': 1, 'color': '#00FF00', 'alpha': 1}]), 'solid-with-stops')
    # Unknown fields remain rejected on the new nested structure.
    reject(lambda b: g(b)['stops'][0].__setitem__('glow', 1), 'unknown-stop-field')

    snapshot = demo.call('POST', '/v1/scenes/gradient/snapshots', {'expectedRevision': 1}, 201)
    job = demo.wait(demo.submit(snapshot, 'gradient-render')['id'])
    png = demo.call('GET', '/v1/artifacts/' + job['artifactId'])
    width, height, rows = decode_png(png)
    assert (width, height) == (120, 40)
    mid = rows[20]
    # Before the 0.5 boundary the fade runs red -> orange. The invisible black
    # of the transparent endpoint must never darken this half: red stays the
    # dominant channel all the way to the boundary.
    for x in range(11, 59):
        r, g, b, a = mid[x]
        assert r >= g and r >= b, f'x={x}: unseen color darkened the fade: {(r, g, b, a)}'
    # Co-located stops at local position 0.5 (world x=60) create a hard,
    # explainable boundary: orange on the left, blue on the right.
    left_rgb = mid[59][:3]
    right_rgb = mid[60][:3]
    assert left_rgb[0] > left_rgb[2] and right_rgb[2] > right_rgb[0] and left_rgb != right_rgb, (left_rgb, right_rgb)
    # Fully transparent endpoint exposes the black base layer beneath.
    assert sum(mid[109][:3]) <= 12 and mid[109][3] == 255
    # Outside the rectangle the untouched cream background remains.
    assert mid[115][:3] == (245, 241, 232)

    # A frozen package rendered through the import path must reproduce the
    # exact same transition in another instance/state store.
    package = demo.call('GET', '/v1/snapshots/' + snapshot['id'] + '/package')
    restored = demo.call('POST', '/v1/imports/gradient-copy', {'name': '迁移的淡出', 'package': package}, 201)
    assert restored['document'] == snapshot['document']
    snap2 = demo.call('POST', '/v1/scenes/gradient-copy/snapshots', {'expectedRevision': 1}, 201)
    job2 = demo.wait(demo.submit(snap2, 'gradient-render-copy')['id'])
    png2 = demo.call('GET', '/v1/artifacts/' + job2['artifactId'])
    assert hashlib.sha256(png).hexdigest() == hashlib.sha256(png2).hexdigest()

    # Legacy two-stop gradient documents stay valid and keep their old shape.
    legacy = sample()
    legacy_scene = demo.call('PUT', '/v1/scenes/emblem', {'name': '双色印记', 'expectedRevision': 0, 'document': legacy})
    legacy_fill = legacy_scene['document']['layers'][0]['fill']
    assert 'stops' not in legacy_fill and legacy_fill['endColor'] == '#32A5A0'
    legacy_snap = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': legacy_scene['revision']}, 201)
    legacy_job = demo.wait(demo.submit(legacy_snap, 'gradient-legacy')['id'])
    legacy_png = demo.call('GET', '/v1/artifacts/' + legacy_job['artifactId'])
    assert legacy_png[:8] == b'\x89PNG\r\n\x1a\n'

    if out:
        Path(out).mkdir(parents=True, exist_ok=True)
        (Path(out) / 'atlas-gradient-stops.png').write_bytes(png)
        (Path(out) / 'atlas-gradient-legacy.png').write_bytes(legacy_png)
    return {'workflow': 'gradient-stops', 'pngDigest': hashlib.sha256(png).hexdigest(),
            'portableDigest': hashlib.sha256(png2).hexdigest(),
            'legacyPngDigest': hashlib.sha256(legacy_png).hexdigest()}


def run(demo, workflow, out):
    if workflow == 'gradient-stops':
        return run_gradient_stops(demo, out)
    d = sample()
    put = lambda doc, rev=0: demo.call('PUT', '/v1/scenes/emblem', {'name': '双色印记', 'expectedRevision': rev, 'document': doc})
    scene = put(d)
    if workflow == 'create-scene':
        assert demo.call('GET', '/v1/scenes/emblem')['document'] == scene['document']
        bad = sample(); bad['layers'][0]['transform']['scaleX'] = 0
        demo.call('PUT', '/v1/scenes/invalid', {'name': '无效几何', 'expectedRevision': 0, 'document': bad}, 400)
    elif workflow == 'revise-scene':
        d['background'] = '#F0F0F0'; assert put(d, 1)['revision'] == 2
        demo.call('PUT', '/v1/scenes/emblem', {'name': '旧版本', 'expectedRevision': 1, 'document': d}, 409)
    elif workflow == 'archive-scene':
        archived = demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': 1, 'state': 'archived'})
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': archived['revision']}, 409)
        assert demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': archived['revision'], 'state': 'active'})['revision'] == 3
    if workflow in ('import-asset', 'retire-asset'):
        asset = demo.call('PUT', '/v1/assets/ribbon', tiny_png(), 201)
        assert asset['width'] == 2 and asset['digest'] == hashlib.sha256(tiny_png()).hexdigest()
        demo.call('PUT', '/v1/assets/broken', b'not png', 400)
        layer = {'id': 'ribbon', 'kind': 'image', 'width': 60, 'height': 60, 'assetId': 'ribbon', 'opacity': 1, 'transform': {'x': 20, 'y': 30, 'scaleX': 1, 'scaleY': 1}}
        d['layers'].append(layer); put(d, 1)
        pinned = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 201)
        demo.call('POST', '/v1/assets/ribbon/retire')
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 409)
        result = demo.wait(demo.submit(pinned)['id']); assert result['state'] == 'succeeded'
        return {'workflow': workflow, 'assetDigest': asset['digest'], 'pinnedRender': result['state']}
    current = demo.call('GET', '/v1/scenes/emblem')
    snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision']}, 201)
    if workflow == 'freeze-scene':
        original_digest = snapshot['digest']; d['background'] = '#000000'; put(d, current['revision'])
        assert demo.call('GET', '/v1/snapshots/' + snapshot['id'])['digest'] == original_digest
    if workflow in ('export-package', 'import-package'):
        package = demo.call('GET', '/v1/snapshots/' + snapshot['id'] + '/package')
        restored = demo.call('POST', '/v1/imports/restored', {'name': '恢复的印记', 'package': package}, 201)
        assert restored['document'] == snapshot['document']
        corrupt = json.loads(json.dumps(package)); corrupt['snapshot']['document']['width'] += 1
        demo.call('POST', '/v1/imports/corrupt', {'name': '损坏包', 'package': corrupt}, 400)
        demo.call('GET', '/v1/scenes/corrupt', status=404)
    if workflow in ('cancel-render', 'retry-render', 'recover-render'):
        heavy = sample(); heavy['width'] = 1500; heavy['height'] = 1500
        heavy['layers'] = [dict(heavy['layers'][0], id=f'sheet-{i}', width=1500, height=1500, transform={'scaleX': 1, 'scaleY': 1}) for i in range(16)]
        put(heavy, current['revision'])
        snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
    job = demo.submit(snapshot)
    if workflow == 'submit-render':
        assert demo.submit(snapshot)['id'] == job['id']
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 2}, 409, {'Idempotency-Key': 'render-one'})
    if workflow == 'revoke-snapshot':
        demo.call('POST', '/v1/snapshots/' + snapshot['id'] + '/revoke')
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 1}, 409, {'Idempotency-Key': 'blocked-after-revoke'})
    if workflow in ('cancel-render', 'retry-render'):
        stopped = demo.call('POST', '/v1/jobs/' + job['id'] + '/cancel')
        assert stopped['state'] == 'cancelled'
        if workflow == 'cancel-render':
            time.sleep(.1); assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'cancelled'
            return {'workflow': workflow, 'state': 'cancelled'}
        job = demo.call('POST', '/v1/jobs/' + job['id'] + '/retry', status=202)
        assert job['attempt'] == 2
    if workflow == 'recover-render':
        demo.wait(job['id'], wanted=('running',)); demo.stop(abrupt=True); demo.start()
    result = demo.wait(job['id'])
    png = demo.call('GET', '/v1/artifacts/' + result['artifactId'])
    assert png[:8] == b'\x89PNG\r\n\x1a\n'
    assert struct.unpack('>II', png[16:24]) == (snapshot['document']['width'], snapshot['document']['height'])
    if out:
        Path(out).mkdir(parents=True, exist_ok=True); (Path(out) / 'atlas-sample.png').write_bytes(png)
    if workflow == 'compare-renders':
        other = demo.wait(demo.submit(snapshot, 'render-two')['id'])
        comparison = demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': other['artifactId']})
        assert comparison['changedPixels'] == 0
        d['background'] = '#000000'; put(d, current['revision'])
        altered = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
        changed = demo.wait(demo.submit(altered, 'render-three')['id'])
        assert demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': changed['artifactId']})['changedPixels'] > 0
    if workflow == 'purge-artifact':
        demo.call('DELETE', '/v1/artifacts/' + result['artifactId'])
        demo.call('GET', '/v1/artifacts/' + result['artifactId'], status=410)
        assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'succeeded'
    if workflow == 'observe-service':
        summary = demo.call('GET', '/v1/status'); assert summary['jobs']['succeeded'] >= 1
        events = demo.call('GET', '/v1/events'); assert any(e['kind'] == 'job.succeeded' for e in events)
    return {'workflow': workflow, 'state': result['state'], 'pngBytes': len(png), 'pngDigest': hashlib.sha256(png).hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', default='submit-render')
    parser.add_argument('--out')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='atlas-walkthrough-') as temp:
        directory = Path(temp); binary = directory / 'atlas'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/atlas'], cwd=ROOT, check=True, timeout=60)
        demo = Demo(binary, directory)
        try:
            print(json.dumps(run(demo, args.workflow, args.out), ensure_ascii=False))
        finally:
            demo.stop(); demo.log.close()


if __name__ == '__main__':
    main()
