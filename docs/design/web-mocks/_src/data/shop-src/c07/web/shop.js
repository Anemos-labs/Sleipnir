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

const params = new URLSearchParams(location.search);
load(Number(params.get('page')) || 1);
