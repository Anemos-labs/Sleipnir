---
name: blender-web-assets
description: Make 3D assets for a website or app with Blender, such as hero renders, transparent product shots, turntable loops, icons and glTF/GLB models for three.js or model-viewer. Use for front-end visuals beyond CSS and SVG.
---
# 3D assets for the web, made in Blender

Load the `blender` skill first for how to run Blender and write portable `bpy`. This skill is about what a front
end needs from it and how to ship it.

## Decide what the page needs

| The page wants | Make | Ship as |
|---|---|---|
| A striking hero or section image | a still render, composed for the layout's aspect ratio | AVIF/WebP plus PNG fallback, 1x and 2x |
| A product or object on any background | a render with `film_transparent = True` | WebP or PNG with alpha |
| Motion without a 3D runtime | a turntable or short loop, 2 to 6 s | muted looping MP4/WebM, or an image sequence sprite, with a poster frame |
| Something the user can rotate | a low-poly, textured model | GLB, compressed, for `<model-viewer>` or three.js |
| Icons or illustrations with depth | flat-lit renders with a consistent camera | PNG/WebP at icon sizes, or traced SVG when it must scale |

Prefer the lightest option that achieves the effect: a still beats a video, a video beats a live 3D scene. Live 3D
is worth it when interaction is the point (configurators, viewers, games).

## Stills that fit the layout

- Set the render size from the layout: a 16:9 hero at 2560x1440 for 2x screens, a square card at 1200x1200.
- Leave negative space where the page puts text. Know which side the headline goes on before placing the camera.
- Match the site's palette: pick the background, rim light and accent material colours from the design tokens, so
  the render looks designed for the page rather than pasted onto it.
- Render once at preview quality, read the PNG, fix, then render final. Keep the script so the image can be
  regenerated when the palette or copy changes.
- Convert and size with the project's tooling (`sharp`, `squoosh`, `cwebp`, `avifenc`, or `ffmpeg`), and give the
  `<img>` explicit `width`/`height` to avoid layout shift. Keep a hero image under about 300 KB.

## Turntables and loops

```python
import math, bpy
scene = bpy.context.scene
scene.frame_start, scene.frame_end, scene.render.fps = 1, 120, 30
pivot = bpy.data.objects.new("Turntable", None)
scene.collection.objects.link(pivot)
subject = bpy.data.objects["Product"]   # the object to show
subject.parent = pivot
pivot.rotation_euler.z = 0;           pivot.keyframe_insert("rotation_euler", index=2, frame=1)
pivot.rotation_euler.z = 2 * math.pi; pivot.keyframe_insert("rotation_euler", index=2, frame=121)
# Linear interpolation so the loop has no ease at the seam: set each keyframe point's interpolation to "LINEAR"
scene.render.image_settings.file_format = "PNG"
scene.render.filepath = "//frames/turn_"
bpy.ops.render.render(animation=True)
```

Frame 121 equals frame 1 and is not rendered, so the loop is seamless. Encode with ffmpeg, for example
`ffmpeg -framerate 30 -i frames/turn_%04d.png -c:v libvpx-vp9 -b:v 0 -crf 34 -pix_fmt yuva420p turn.webm`
(keeps alpha) and an H.264 MP4 without alpha as a fallback. Use it as
`<video autoplay muted loop playsinline poster="turn.webp">`, and respect `prefers-reduced-motion` by showing the
poster instead.

## GLB models for three.js and model-viewer

Budget first: under about 50k triangles and 2 MB for a model on a landing page; textures 1024 or 2048 px.

1. Model at real scale, origin at the bottom centre, front facing -Y in Blender (towards the
   Front view camera), which the exporter turns into glTF's +Z forward.
2. Apply transforms, remove hidden helper objects, merge what does not move independently.
3. Use Principled BSDF only, with image textures plugged directly into its inputs; glTF cannot carry procedural
   node setups. Bake procedural materials to images first (Cycles bake to an image texture on a UV map).
4. Export. Read the operator's arguments for this Blender version first (see the `blender` skill), then:

```python
bpy.ops.export_scene.gltf(
    filepath="public/models/product.glb", export_format="GLB",
    use_selection=False, export_apply=True, export_yup=True,
    export_animations=True, export_draco_mesh_compression_enable=False,
)
```

5. Compress outside Blender, where the tools are better: `npx @gltf-transform/cli optimize in.glb out.glb
   --compress meshopt --texture-compress webp`. Draco or Meshopt need the matching decoder on the page.
6. Verify the file, not the intent: load it in the project's page (or `npx @google/model-viewer` locally / a three.js
   test page), screenshot it in a browser, and read the screenshot. Check scale, orientation, materials and that
   textures loaded.

On the page, `<model-viewer>` is the simplest viewer:

```html
<model-viewer src="models/product.glb" poster="img/product.webp" alt="..." camera-controls auto-rotate loading="lazy"></model-viewer>
```

Use three.js with `GLTFLoader` (plus `MeshoptDecoder` or
`DRACOLoader`) when the scene is custom. Always give a poster image and an `alt` text, lazy-load below the fold, and
keep the page usable when WebGL is unavailable.

## Licences

Everything in a shipped asset must be redistributable. Poly Haven assets are CC0. Sketchfab and Poly Pizza models
carry per-model licences: record each model's author and licence in the repository (for example
`assets/CREDITS.md`) and credit CC-BY authors on the site. Do not ship assets whose licence you could not confirm.
