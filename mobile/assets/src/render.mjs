// Renders the app icon PNGs from the SVG sources in this directory.
//
//   node mobile/assets/src/render.mjs                  # writes mobile/assets/*.png
//   node mobile/assets/src/render.mjs --preview <dir>  # also writes review previews to <dir>
//
// Uses sharp (a devDependency of @talanta/mobile). Paths are resolved from this
// file, so it works from any cwd. Edit the .svg files, re-run, commit the PNGs.
//
// Brand colours: Emerald Ink #064E3B, Champagne #F8E7C9.
import { mkdirSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';

const SRC = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(SRC, '..');
const EMERALD_INK = '#064E3B';

const svg = (name) => readFileSync(join(SRC, `${name}.svg`));
const raster = (name, size) => sharp(svg(name), { density: 72 * (size / 1024) * 4 }).resize(size, size);

async function write(file, pipeline) {
  const info = await pipeline.png({ compressionLevel: 9 }).toFile(file);
  console.log(`${file}  ${info.width}x${info.height}  ${info.channels === 4 ? 'alpha' : 'opaque'}`);
}

async function renderAssets() {
  // iOS rejects icons with an alpha channel: flatten and drop it.
  await write(join(OUT, 'icon.png'), raster('icon', 1024).flatten({ background: EMERALD_INK }).removeAlpha());
  await write(join(OUT, 'adaptive-icon.png'), raster('adaptive-foreground', 1024).ensureAlpha());
  await write(join(OUT, 'adaptive-icon-monochrome.png'), raster('adaptive-monochrome', 1024).ensureAlpha());
  await write(join(OUT, 'splash-icon.png'), raster('splash-icon', 1024).ensureAlpha());
  await write(join(OUT, 'favicon.png'), raster('favicon', 64).ensureAlpha());
}

// Previews: what a launcher actually shows. Launchers display the centre
// 72/108 of the adaptive layers, then apply their mask.
async function renderPreviews(dir) {
  mkdirSync(dir, { recursive: true });
  const SIZE = 512;
  const visible = Math.round((1024 * 72) / 108);
  const offset = Math.round((1024 - visible) / 2);

  const circle = `<circle cx="256" cy="256" r="256"/>`;
  const pts = [];
  for (let i = 0; i < 360; i++) {
    const t = (i * Math.PI) / 180;
    const sx = Math.sign(Math.cos(t)) * Math.abs(Math.cos(t)) ** 0.5;
    const sy = Math.sign(Math.sin(t)) * Math.abs(Math.sin(t)) ** 0.5;
    pts.push(`${(256 + 256 * sx).toFixed(2)},${(256 + 256 * sy).toFixed(2)}`);
  }
  const squircle = `<polygon points="${pts.join(' ')}"/>`;
  const mask = (shape) => Buffer.from(`<svg xmlns="http://www.w3.org/2000/svg" width="${SIZE}" height="${SIZE}">${shape}</svg>`);

  const launcher = async (layer, background, shape, tint) => {
    let fg = sharp(await raster(layer, 1024).ensureAlpha().png().toBuffer())
      .extract({ left: offset, top: offset, width: visible, height: visible })
      .resize(SIZE, SIZE);
    if (tint) {
      // Themed icons: keep only the alpha channel and paint it with the tint.
      const alpha = await fg.extractChannel('alpha').toBuffer();
      fg = sharp({ create: { width: SIZE, height: SIZE, channels: 3, background: tint } }).joinChannel(alpha);
    }
    const flat = await sharp({ create: { width: SIZE, height: SIZE, channels: 4, background } })
      .composite([{ input: await fg.png().toBuffer() }])
      .png()
      .toBuffer();
    return sharp(flat).composite([{ input: mask(shape), blend: 'dest-in' }]);
  };

  await write(join(dir, 'icon-512.png'), sharp(join(OUT, 'icon.png')).resize(SIZE, SIZE));
  await write(join(dir, 'icon-48.png'), sharp(join(OUT, 'icon.png')).resize(48, 48));
  await write(join(dir, 'adaptive-circle-512.png'), await launcher('adaptive-foreground', EMERALD_INK, circle));
  await write(join(dir, 'adaptive-squircle-512.png'), await launcher('adaptive-foreground', EMERALD_INK, squircle));
  // Sample themed palette: a light tint of Emerald Ink behind an Emerald Ink mark.
  await write(join(dir, 'adaptive-monochrome-circle-512.png'), await launcher('adaptive-monochrome', '#D5E2DE', circle, EMERALD_INK));
}

await renderAssets();
const i = process.argv.indexOf('--preview');
if (i !== -1) {
  const dir = process.argv[i + 1];
  if (!dir) throw new Error('--preview needs a directory');
  await renderPreviews(resolve(dir));
}
