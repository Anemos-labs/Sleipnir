const grid = document.querySelector('#items');
const pager = document.querySelector('#pager');
let page = 1;

async function load(n) {
  const res = await fetch(`/items?page=${n}&size=12`);
  const data = await res.json();
  page = data.page;
  grid.replaceChildren(...data.items.map(card));
  pager.replaceChildren(...pagerButtons(data.page, data.pages));
}

function card(item) {
  const el = document.createElement('article');
  el.className = 'card';
  const name = document.createElement('h2');
  name.textContent = item.name;
  const price = document.createElement('p');
  price.textContent = `$${(item.price_cents / 100).toFixed(2)}`;
  el.append(name, price);
  return el;
}

function pagerButtons(current, pages) {
  const out = [];
  for (let n = 1; n <= pages; n++) {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = String(n);
    if (n === current) b.setAttribute('aria-current', 'page');
    b.addEventListener('click', () => load(n));
    out.push(b);
  }
  return out;
}

const params = new URLSearchParams(location.search);
load(Number(params.get('page')) || 1);
