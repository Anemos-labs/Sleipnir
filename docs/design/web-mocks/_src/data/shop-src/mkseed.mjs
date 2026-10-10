// 48 items, ids itm-001..itm-048 (sorted order = id order). base: dollars as "price"; c05: integer cents as "price_cents".
const names = [
 ["Ash cutting board",24.00],["Birch butter knife",8.50],["Brass candle snuffer",14.00],["Cast iron trivet",18.50],["Cedar spoon rack",22.00],["Ceramic egg cup",6.00],
 ["Charcoal soap bar",5.50],["Cherry rolling pin",19.00],["Copper measuring cups",32.00],["Cotton tea towel",9.00],["Dovetail recipe box",27.50],["Enamel camp mug",11.00],
 ["Felt coaster set",7.50],["Flax apron",29.00],["Glass spice jar",4.50],["Hand-forged ladle",26.00],["Hemp market bag",13.00],["Herb scissors",12.50],
 ["Iron skillet 20cm",38.00],["Juniper salt cellar",15.00],["Knit pot holder",8.00],["Larch bread board",21.00],["Linen napkins x4",24.50],["Maple honey dipper",5.00],
 ["Marble pastry slab",46.00],["Mortar and pestle",34.00],["Oak bowl 24cm",36.00],["Olive wood tongs",10.50],["Pine cheese knife",9.50],["Pewter jug 1L",42.00],
 ["Rye straw trivet",6.50],["Rowan scoop",7.00],["Sage smudge bundle",5.50],["Slate serving plate",17.00],["Smoked paprika tin",6.50],["Stoneware butter dish",23.00],
 ["Sycamore salad hands",16.00],["Tin lantern",28.00],["Tinned mackerel box",12.00],["Walnut pepper mill",31.00],["Wax food wrap",11.50],["Willow basket small",19.50],
 ["Wool felt slippers",33.00],["Yarrow tea blend",8.50],["Yew toast rack",14.50],["Zinc watering can",25.00],["Linseed oil 250ml",9.00],["Beeswax polish",7.50]];
if (names.length !== 48) throw new Error('need 48, have ' + names.length);
const rows = names.map(([name, price], i) => ({ id: 'itm-' + String(i + 1).padStart(3, '0'), name, price }));
import fs from 'node:fs';
const fmtBase = (o) => '[\n' + o.map((r) => `  {"id": "${r.id}", "name": "${r.name}", "price": ${r.price.toFixed(2)}}`).join(',\n') + '\n]\n';
const fmtC = (o) => '[\n' + o.map((r) => `  {"id": "${r.id}", "name": "${r.name}", "price_cents": ${Math.round(r.price * 100)}}`).join(',\n') + '\n]\n';
fs.writeFileSync('base/seed/items.json', fmtBase(rows));
fs.writeFileSync('c05/seed/items.json', fmtC(rows));
