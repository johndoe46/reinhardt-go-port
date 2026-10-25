const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');

let Card, page = 'album', played, queued, fetchRequest, observer, frame;
let compiled = 0, updated = 0;
let networkCalls = 0;
const cache = new Map(), requestCalls = [];
const settle = () => new Promise(setImmediate);
const line = width => ({
    clientWidth: 140,
    firstElementChild: {scrollWidth: width},
    style: new Map(),
    toggleAttribute(name, enabled) { this[name] = enabled; }
});
const short = line(80), long = line(350);
for (const element of [short, long]) element.style.setProperty = element.style.set;
const menu = {replaceChildren(item) { this.status = item.child.textContent; }};
const menuButton = {contains(element) { return element === this; }};
const menuEvent = {target: {closest: () => menuButton}, relatedTarget: null};
class Element {
    constructor() {
        this.listeners = new Map();
        this.dataset = {trackid: '42', removeFromTag: '7', menuUrl: '/prefix/music_file_dropdown/', menuLoading: 'Loading', menuError: 'Try again'};
        this.isConnected = true;
    }
    addEventListener(type, listener, {signal}) {
        this.listeners.set(type, listener);
        signal.addEventListener('abort', () => this.listeners.delete(type), {once: true});
    }
    querySelectorAll() { return [short, long]; }
    querySelector(selector) { return selector === '.dropdown-menu' ? menu : menuButton; }
}
vm.runInNewContext(fs.readFileSync(0, 'utf8'), {
    HTMLElement: Element, AbortController, URLSearchParams,
    customElements: {define(name, component) { assert.equal(name, 'track-card'); Card = component; }},
    requestAnimationFrame(callback) { frame = callback; return 1; },
    cancelAnimationFrame() { frame = null; },
    ResizeObserver: class {
        constructor(callback) { this.callback = callback; this.observed = []; observer = this; }
        observe(element) { this.observed.push(element); }
        disconnect() { this.observed = []; }
    },
    document: {
        getElementById() { return {value: page}; },
        createElement() { return {setAttribute() {}, append(child) { this.child = child; }}; }
    },
    playNext(track) { played = track; },
    playQueueItem(button) { queued = button; },
    up: {
        hello(element) { assert.equal(element, menu); compiled++; },
        request(url, options) {
            requestCalls.push(options);
            assert.equal(options.cache, true);
            assert.equal(options.origin, menuButton);
            if (cache.has(url)) return cache.get(url).request;
            networkCalls++;
            let abort;
            const request = new Promise((resolve, reject) => {
                abort = () => reject(new Error('Aborted'));
                Promise.resolve().then(() => fetchRequest(url, options)).then(resolve, reject);
            });
            request.aborted = false;
            request.abort = () => { request.aborted = true; abort(); };
            const entry = {request};
            cache.set(url, entry);
            request.then(response => { entry.response = response; }, () => cache.delete(url));
            return request;
        },
        cache: {
            evict(url) { cache.delete(url); },
            expire() { for (const entry of cache.values()) if (entry.response) entry.response.expired = true; }
        }
    },
    bootstrap: {Dropdown: {getInstance(button) { assert.equal(button, menuButton); return {update() { updated++; }}; }}}
});

(async () => {
    const card = new Card();
    card.connectedCallback();
    frame();
    assert.equal(observer.observed.length, 2);
    assert.equal(short['data-overflow'], false);
    assert.equal(long['data-overflow'], true);
    assert.equal(long.style.get('--marquee-distance'), '-210px');
    long.clientWidth = 400;
    observer.callback();
    assert.equal(long['data-overflow'], false, 'widening the card should stop the marquee');
    long.clientWidth = 140;
    card.listeners.get('focusin')({target: {closest: () => null}});
    assert.equal(long['data-overflow'], true, 'keyboard focus should measure overflow');

    const button = {};
    const click = () => card.listeners.get('click')({target: {closest: () => button}});
    click();
    assert.equal(played, 42);
    page = 'queue';
    click();
    assert.equal(queued, button, 'queue listen must select the existing item');

    fetchRequest = async (url, options) => {
        const parsed = new URL(url, 'http://localhost');
        assert.equal(parsed.pathname, '/prefix/music_file_dropdown/');
        assert.equal(parsed.searchParams.get('q'), '42');
        assert.equal(parsed.searchParams.get('tag'), '7');
        assert.equal(parsed.searchParams.get('context'), 'queue');
        assert.equal(parsed.searchParams.get('grid'), '1');
        return {text: '<li>Remaining actions</li>'};
    };
    card.listeners.get('pointerover')({target: {closest: () => null}});
    assert.equal(networkCalls, 0, 'hovering other card content must not preload actions');
    card.listeners.get('pointerover')(menuEvent);
    await settle();
    assert.equal(networkCalls, 1, 'hovering the menu must preload through Unpoly');
    assert.equal(requestCalls[0].background, true);
    assert.equal(menu.innerHTML, undefined, 'hovering must not render or open the menu');
    await card.loadMenu();
    assert.equal(networkCalls, 1, 'opening must reuse the completed hover preload');
    assert.equal(requestCalls.at(-1).background, false);
    assert.equal(menu.innerHTML, '<li>Remaining actions</li>');
    assert.equal(compiled, 1);
    assert.equal(updated, 1);
    // Mutations expire the Unpoly cache. The component must refresh stale menu
    // HTML instead of continuing to show outdated favorite/tag actions.
    for (const entry of cache.values()) entry.response.expired = true;
    fetchRequest = async () => ({text: '<li>Updated favorite action</li>'});
    await Promise.all([card.requestMenu(true), card.loadMenu()]);
    assert.equal(networkCalls, 2);
    assert.equal(menu.innerHTML, '<li>Updated favorite action</li>');
    cache.clear();
    fetchRequest = async () => { throw new Error('HTTP 500'); };
    card.listeners.get('pointerover')(menuEvent);
    await settle();
    assert.notEqual(menu.status, 'Try again', 'failed hover preloads must stay silent');
    await card.loadMenu();
    assert.equal(menu.status, 'Try again');

    let resolveResponse;
    fetchRequest = () => new Promise(resolve => { resolveResponse = resolve; });
    card.listeners.get('focusin')(menuEvent);
    await settle();
    const callsBeforeOpening = networkCalls;
    const pending = card.loadMenu();
    await settle();
    assert.equal(networkCalls, callsBeforeOpening, 'opening must share an in-flight focus preload');
    const request = [...card.menuRequests][0];
    card.disconnectedCallback();
    card.isConnected = false;
    assert.equal(request.aborted, true);
    assert.equal(observer.observed.length, 0);
    assert.equal(card.listeners.size, 0);
    resolveResponse({text: 'stale actions'});
    await pending;
    assert.equal(menu.innerHTML, '<li>Updated favorite action</li>', 'removed cards must ignore pending responses');
    card.isConnected = true;
    card.connectedCallback();
    frame();
    assert.equal(card.listeners.size, 4, 'reconnecting must restore one set of listeners');
    card.disconnectedCallback();
})().catch(error => { console.error(error); process.exitCode = 1; });
