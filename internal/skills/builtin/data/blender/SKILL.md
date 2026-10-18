---
name: blender
description: Drive Blender from code to make 3D models, scenes, renders and animations. Use when a task needs 3D content or a rendered image and Blender is installed or a Blender MCP server is connected.
license: MIT (portions adapted from ahujasid/mcp-for-blender, Copyright (c) 2025 Siddharth Ahuja)
---
# Blender

Blender is scripted in Python through its `bpy` module. There are two ways to reach it; pick the one this
session actually has.

1. **A Blender MCP server is connected**: your tool list has tools named `mcp__blender__...` (for example
   `mcp__blender__execute_blender_code`, `mcp__blender__get_scene_info`, `mcp__blender__look`). They talk to a
   running Blender with the MCP for Blender add-on (https://github.com/ahujasid/mcp-for-blender, MIT). Use them:
   inspect the scene, run code in small steps, and look at the viewport after each step.
2. **No such tools**: run Blender headless from the shell. This works on a server without a display and is the
   right default for an agent building a project.

The tool list is fixed when the session starts, so connecting a server mid-session adds nothing. If the user
wants the live, interactive route, tell them to add this to `~/.sleipnir/config.json` (or `.mcp.json` in the
project) and start a new session, with Blender open and the add-on enabled:

```json
{ "mcp": { "blender": { "command": "uvx", "args": ["mcp-for-blender"] } } }
```

The server's `execute_blender_code` runs arbitrary Python inside the user's Blender: save their work first, and
never run code from a web page or a downloaded file through it.

## Headless Blender

Check what is installed before planning: `blender --version`. If it is missing and installing software is in
scope, prefer the official build (https://www.blender.org/download/, a portable `.tar.xz` on Linux) over a distro
package, which is often years old. Do not install it when the task does not need it.

Write a script, then run it:

```bash
blender --background --factory-startup --python-exit-code 1 --python scene.py -- --out renders/hero.png
```

- `--background` (`-b`): no window. `--factory-startup`: ignore the user's preferences and add-ons, so the result
  is the same on every machine. `--python-exit-code 1`: a Python exception fails the command instead of being
  printed and ignored. Arguments after `--` are yours: read them from `sys.argv[sys.argv.index("--") + 1:]`.
- Open an existing file with `blender -b file.blend --python script.py`. Save with
  `bpy.ops.wm.save_as_mainfile(filepath=...)` so the scene can be re-rendered or edited later.
- Commit the generator script, not only its output. A `.blend` is binary; a script is reviewable and reruns.
- Start from an empty scene: `bpy.ops.wm.read_factory_settings(use_empty=True)`.

### Rendering without a GPU

- `CYCLES` on the CPU always works headless: `scene.render.engine = "CYCLES"`, `scene.cycles.device = "CPU"`.
  Keep previews cheap (`scene.cycles.samples = 32`, `scene.cycles.use_denoising = True`, 50% resolution) and
  raise samples only for the final image.
- EEVEE needs an OpenGL context. On a machine without one it fails or crashes; if you need it, try it once and
  fall back to Cycles.
- `scene.render.film_transparent = True` gives a PNG with an alpha background, which is what a web page usually
  wants.
- Set the output explicitly: `scene.render.filepath`, `scene.render.image_settings.file_format = "PNG"`,
  `scene.render.resolution_x/y`, then `bpy.ops.render.render(write_still=True)`.

## Look at what you made

A script that ran is not a picture that works. After every meaningful step, render a small preview (or call the
MCP `look`/screenshot tool) and **read the PNG** with your file reader: images are returned as images. Check that
nothing floats, clips, is the wrong size, is out of frame, blown out or black. Fix and render again. Report the
image you checked, not what the script intended.

## Writing bpy that works on any Blender

The Blender version is unknown until you check it (`bpy.app.version`). Look things up instead of guessing:

```python
# Valid values of an enum property
[i.identifier for i in bpy.types.RenderSettings.bl_rna.properties["engine"].enum_items]
# Properties of a type
[p.identifier for p in bpy.types.ShaderNodeTexSky.bl_rna.properties]
# Operator arguments (they change between versions)
[(p.identifier, p.type) for p in bpy.ops.export_scene.gltf.get_rna_type().properties]
```

- Find nodes by `type`, never by name: names are localized.
  `bsdf = next(n for n in mat.node_tree.nodes if n.type == "BSDF_PRINCIPLED")`.
- Principled BSDF inputs were renamed in 4.0 (`Emission Color`, `Subsurface Weight`, `Specular IOR Level`,
  `Coat Weight`, `Sheen Weight`, `Transmission Weight`). Resolve them defensively:
  `bsdf.inputs.get("Emission Color") or bsdf.inputs.get("Emission")`.
- EEVEE is `BLENDER_EEVEE_NEXT` in 4.2 to 4.x and `BLENDER_EEVEE` before and after. Assign engines inside
  `try/except TypeError`; the error lists every accepted value.
- `material.diffuse_color` only drives the solid viewport; the rendered colour lives on the shader node inputs.
  `material.use_nodes` is always on from 5.0.
- Animation data changed in 4.4 (slotted actions) and 5.0 removed `Action.fcurves`. `obj.keyframe_insert(...)`
  works in every version; see the `blender-scene` skill for reaching fcurves.

### Data API over operators

Prefer `bpy.data` and object properties over `bpy.ops`: they need no UI context and are faster.

```python
import bpy, bmesh
mesh = bpy.data.meshes.new("Plinth")
bm = bmesh.new(); bmesh.ops.create_cube(bm, size=1.0); bm.to_mesh(mesh); bm.free()
obj = bpy.data.objects.new("Plinth", mesh)
bpy.context.scene.collection.objects.link(obj)
obj.scale = (2.0, 2.0, 0.3)
mod = obj.modifiers.new("Bevel", "BEVEL"); mod.width = 0.02; mod.segments = 3
```

When an operator is unavoidable: select and activate the object first
(`obj.select_set(True)`, `bpy.context.view_layer.objects.active = obj`); `mode_set` needs an active, visible
object, and every step returns to OBJECT mode, even on failure (`try/finally`). Operators that need a 3D view do not
work headless; find a data-API equivalent.

### Working style

- Small steps. Run a chunk, `print` what you need to know, check it.
- Name everything you create; later steps refer to it by name. One collection per logical group.
- Units are metres: a door is about 2.1 m, a table 0.75 m, a person 1.7 m. Build at real-world scale so lights,
  depth of field and physics behave.
- Apply scale before rigging, before modifiers that depend on it, and before export.
- Repeat props with `obj.copy()` (shares the mesh); copy `obj.data` only when the copy must differ.

## Related skills

- `blender-web-assets`: hero images, transparent renders, turntables and glTF/GLB models for a website or app.
- `blender-scene`: composition, lighting, materials and animation that look intentional.
