---
name: blender-scene
description: Compose, light, texture and animate a Blender scene so the result looks intentional. Use after the blender skill when building a scene, product shot, environment, level blockout or animation.
license: MIT (adapted from the guides of ahujasid/mcp-for-blender, Copyright (c) 2025 Siddharth Ahuja)
---
# Scene design in Blender

Load the `blender` skill first for running Blender and portable `bpy`. Names of MCP tools below (`get_scene_info`,
`look`, `search_assets`, `generate_3d`) apply only when a Blender MCP server is connected; headless, render a
preview and read the PNG instead.

## Workflow

1. Know what is there and its scale before adding anything.
2. Block out with primitives at real-world size: layout, big shapes, camera. Check proportions from the camera
   before spending time on detail.
3. Replace blockout pieces with real assets or modelled geometry, biggest and most visible first.
4. Light, then materials, then small props.
5. Judge the image, not your intentions: is anything floating, clipping, the wrong size, or hidden from camera?

## Getting assets

- **Script it** for simple or procedural geometry: walls, floors, shelves, stairs, fences, arrays, scattering.
- **Poly Haven** (CC0): HDRIs, PBR textures for large surfaces, generic realistic props.
- **Sketchfab**: specific real-world things; check each model's licence and face count.
- **Poly Pizza**: stylised low-poly props; credit CC-BY creators.
- **Generated models** (when an MCP server offers generation): one hero object per generation, never a whole scene.
  Generate once and duplicate for repeats.

Match the style: do not mix low-poly props with photoreal scans in one frame.

## After every import

Imported and generated models arrive at arbitrary scale, rotation and origin. Use the world bounding box:

```python
from mathutils import Vector
corners = [obj.matrix_world @ Vector(c) for c in obj.bound_box]
min_z = min(c.z for c in corners); height = max(c.z for c in corners) - min_z
obj.scale *= 0.75 / height      # scale to a real size, here a 0.75 m table
bpy.context.view_layer.update()
corners = [obj.matrix_world @ Vector(c) for c in obj.bound_box]
obj.location.z -= min(c.z for c in corners)   # stand it on the ground
```

Rotate it to face the right way (generators usually face -Y or +Y) and check it does not intersect its neighbours.

## Composition

- Decide the camera early and compose for it. Focal length 35 to 50 mm for natural views, 85 mm or more for product
  shots, 24 mm or wider for interiors.
- Foreground, midground, background. Leave negative space; put the subject on a third.
- Vary scale and rotation of repeated props slightly; perfect grids read as fake.
- Ground contact sells realism: props sit on surfaces, small things cluster against larger ones.

## Lighting

- Start with an HDRI for ambient light and reflections (strength 0.5 to 1.5), then a key light (sun or area) for
  shape and shadows.
- Products: three-point lighting. Key, fill at lower power, rim behind to separate the silhouette.
- Interiors: area lights in the windows plus practicals (lamps with emission).
- Blown-out white or pure black areas mean light power is off; fix the lights before touching exposure. Use the
  `AgX` view transform (Blender 4+) or `Filmic` for a natural highlight roll-off.

## Materials

```python
mat = bpy.data.materials.new("Brushed Steel")
mat.use_nodes = True   # no-op in 5.0+, where it is always on
bsdf = next(n for n in mat.node_tree.nodes if n.type == "BSDF_PRINCIPLED")
bsdf.inputs["Base Color"].default_value = (0.6, 0.6, 0.62, 1)
bsdf.inputs["Metallic"].default_value = 1.0
bsdf.inputs["Roughness"].default_value = 0.35
obj.data.materials.clear(); obj.data.materials.append(mat)
```

| Material | Base colour (linear) | Metallic | Roughness |
|---|---|---|---|
| Painted wall | 0.5 to 0.7 grey | 0 | 0.8 to 0.9 |
| Varnished wood | textured | 0 | 0.3 to 0.5 |
| Plastic | any | 0 | 0.3 to 0.6 |
| Polished metal | 0.9 | 1 | 0.05 to 0.2 |
| Rubber | 0.02 to 0.05 | 0 | 0.8 |
| Glass | white | 0 | 0, Transmission 1, IOR 1.45 |

- Nothing in nature is pure black or pure white as a base colour; keep it between about 0.02 and 0.9.
- Textures need UVs. Primitives have them; scripted meshes need a projection (Smart UV Project, or box projection
  with `Texture Coordinate > Object` into a Mapping node and the Image Texture node's projection set to `BOX`).
- Tiling: Mapping scale = surface size in metres / texture size in metres. Large surfaces need large-coverage
  textures or they repeat visibly.
- Roughness, metallic and normal maps are Non-Color data: `img.colorspace_settings.name = "Non-Color"`.
- A little noise in roughness (Noise Texture through a Color Ramp) makes any flat material look less CG.
- Material colours only show in Material Preview or Rendered shading, never in Solid.

## Animation

Keyframe properties directly; it works in every version:

```python
scene = bpy.context.scene
scene.frame_start, scene.frame_end, scene.render.fps = 1, 120, 24
obj.location = (0, 0, 0); obj.keyframe_insert("location", frame=1)
obj.location = (0, 0, 2); obj.keyframe_insert("location", frame=24)
```

Reaching fcurves (4.4 added slotted actions, 5.0 removed `Action.fcurves`):

```python
def fcurves(obj):
    ad = obj.animation_data
    if not ad or not ad.action:
        return []
    if hasattr(ad, "action_slot"):
        from bpy_extras import anim_utils
        cb = anim_utils.action_get_channelbag_for_slot(ad.action, ad.action_slot)
        return list(cb.fcurves) if cb else []
    return list(ad.action.fcurves)
```

Then set each keyframe point's `interpolation`: `BEZIER` with `easing` for organic motion, `LINEAR` for mechanical
motion and seamless loops, `CONSTANT` for stepped blocking.

- Timing and spacing matter more than poses. Ease in and out of holds.
- Anticipation before big moves, overshoot and settle after them, secondary parts offset by 2 to 4 frames.
- Limbs and thrown objects travel on arcs.
- Loops: make the first and last key identical and add a Cycles modifier (`fc.modifiers.new("CYCLES")`), or end
  the frame range one frame before the duplicate key.
- Cameras: animate a parent empty, or use a Track To constraint on a target empty for smooth follow shots.
- One still cannot show motion: render a strip of evenly spaced frames and check for popping, interpenetration,
  sliding feet and the loop seam.

## Game levels and engine export

- Agree the player's size first (a 1.8 m capsule named `PlayerScale` kept in the scene); doorways 1.2 to 1.5 m wide,
  corridors 3 to 4 m, steps 0.2 m rise and 0.3 m run, a 1 m grid with modules on 2 or 4 m.
- Grey-box the whole level before any art; one collection per area; snap to the grid.
- Modular pieces with the origin at a corner or bottom centre, placed as linked duplicates or collection instances.
- Apply transforms on every exported mesh. Name for the engine: Unreal `SM_` meshes and `UCX_<mesh>` collision,
  Godot `-col`/`-colonly` suffixes. Keep collision to boxes and convex hulls.
- Export glTF (`export_scene.gltf`) for Godot, the web and most engines, FBX for Unity and Unreal; read the
  operator's arguments first, they change between versions.
