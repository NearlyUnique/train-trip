function wordBoundaryMatch(name, q) {
  return name.toLowerCase().split(/[\s&()/,-]+/).some(w => w.startsWith(q));
}

function buildResults(stations, raw) {
  const q = raw.trim().toLowerCase();
  if (!q) return [];
  const crsHits  = [];
  const nameHits = [];
  for (const s of stations) {
    if (s.crs.toLowerCase().startsWith(q)) {
      crsHits.push(s);
    } else if (wordBoundaryMatch(s.name, q)) {
      nameHits.push(s);
    }
  }
  return [...crsHits, ...nameHits].slice(0, 10);
}

function initAutocomplete(stations) {
  const searchInput = document.getElementById('origin-input');
  const hiddenInput = document.getElementById('origin');
  const list        = document.getElementById('ac-list');
  const form        = document.getElementById('search-form');

  let activeIdx = -1;

  function closeList() {
    list.hidden = true;
    activeIdx   = -1;
    searchInput.setAttribute('aria-expanded', 'false');
  }

  function renderList(results) {
    activeIdx = -1;
    list.innerHTML = '';
    for (const s of results) {
      const li = document.createElement('li');
      li.dataset.crs  = s.crs;
      li.dataset.name = s.name;
      const code = document.createElement('span');
      code.className   = 'ac-code';
      code.textContent = s.crs;
      li.appendChild(code);
      li.appendChild(document.createTextNode(' ' + s.name));
      li.addEventListener('mousedown', e => { e.preventDefault(); select(s); });
      list.appendChild(li);
    }
    list.hidden = false;
    searchInput.setAttribute('aria-expanded', 'true');
  }

  function select(s) {
    searchInput.value = s.name;
    hiddenInput.value = s.crs;
    searchInput.setCustomValidity('');
    closeList();
  }

  function setActive(items, idx) {
    items.forEach((li, i) => li.classList.toggle('ac-active', i === idx));
    if (idx >= 0) {
      searchInput.setAttribute('aria-activedescendant', items[idx].id || '');
    }
  }

  searchInput.addEventListener('input', () => {
    hiddenInput.value = '';
    const results = buildResults(stations, searchInput.value);
    if (results.length) {
      renderList(results);
    } else {
      closeList();
    }
  });

  searchInput.addEventListener('keydown', e => {
    const items = [...list.querySelectorAll('li')];
    if (!items.length) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      activeIdx = Math.min(activeIdx + 1, items.length - 1);
      setActive(items, activeIdx);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      activeIdx = Math.max(activeIdx - 1, 0);
      setActive(items, activeIdx);
    } else if (e.key === 'Enter' && activeIdx >= 0) {
      e.preventDefault();
      const li = items[activeIdx];
      select({ crs: li.dataset.crs, name: li.dataset.name });
    } else if (e.key === 'Escape') {
      closeList();
    }
  });

  searchInput.addEventListener('blur', () => setTimeout(closeList, 150));

  form.addEventListener('submit', e => {
    if (!hiddenInput.value) {
      e.preventDefault();
      searchInput.setCustomValidity('Select a station from the list');
      searchInput.reportValidity();
    }
  });
}

if (typeof module !== 'undefined') {
  module.exports = { wordBoundaryMatch, buildResults };
}
