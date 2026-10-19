import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("../internal/assets/static/fullscreen-player.js", import.meta.url), "utf8");
const {FullscreenPlayer} = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);

class Element extends EventTarget {
    dataset = {};
    hidden = false;
    value = "waveform";
    textContent = "";
    open = false;
    focus() { document.activeElement = this; }
    showModal() { this.open = true; }
    close() { this.open = false; this.dispatchEvent(new Event("close")); }
}

function fixture(context) {
    context.mock.timers.enable({apis: ["setTimeout"]});
    const previousDocument = globalThis.document;
    const elements = Object.fromEntries([
        "canvas", "#fullscreen-visualization", ".fullscreen-player__effect", ".fullscreen-player__status",
        ".fullscreen-player__next", ".fullscreen-player__close",
    ].map(selector => [selector, new Element()]));
    elements[".fullscreen-player__status"].dataset = {loading: "loading", unavailable: "unavailable"};
    const dialog = new Element();
    dialog.querySelector = selector => elements[selector];
    const document = new Element();
    document.getElementById = id => id === "fullscreen-player" ? dialog : null;
    document.body = {classList: {add() {}, remove() {}}};
    globalThis.document = document;
    context.after(() => {
        fullscreen.disconnect();
        globalThis.document = previousDocument;
    });
    const overlays = [];
    const player = {oscilloscope: {setOverlay: (...args) => overlays.push(args)}};
    const fullscreen = new FullscreenPlayer(player);
    context.mock.method(fullscreen, "updateTrack", () => {});
    fullscreen.connect();
    return {fullscreen, dialog, overlays};
}

function effect() {
    return {disposed: false, dispose() { this.disposed = true; }};
}

test("the waveform is the default and does not load Butterchurn", context => {
    const {fullscreen, dialog, overlays} = fixture(context);
    const loader = context.mock.method(fullscreen, "loadVisualization");
    fullscreen.toggle();
    assert.equal(fullscreen.visualizationSelect.value, "waveform");
    assert.equal(loader.mock.callCount(), 0);
    assert.deepEqual(overlays.at(-1), [dialog.querySelector("canvas")]);
});

test("switching back or closing releases the effect and restores the waveform", async context => {
    const {fullscreen, dialog, overlays} = fixture(context);
    const effects = [];
    context.mock.method(fullscreen, "loadVisualization", async () => {
        const instance = effect();
        effects.push(instance);
        return instance;
    });
    fullscreen.toggle();
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    assert.equal(fullscreen.effectButton.hidden, false);
    assert.equal(overlays.at(-1)[1], effects[0]);
    fullscreen.visualizationSelect.value = "waveform";
    await fullscreen.updateVisualization();
    assert.equal(effects[0].disposed, true);
    assert.equal(fullscreen.effectButton.hidden, true);
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    dialog.close();
    assert.equal(effects[1].disposed, true);
    assert.deepEqual(overlays.at(-1), [null]);
    assert.equal(fullscreen.visualizationSelect.value, "butterchurn");
});

for (const close of [true, false]) {
    test(`a late download cannot reopen an effect after ${close ? "closing" : "changing selection"}`, async context => {
        const {fullscreen, dialog, overlays} = fixture(context);
        let resolve;
        context.mock.method(fullscreen, "loadVisualization", () => new Promise(done => { resolve = done; }));
        fullscreen.toggle();
        fullscreen.visualizationSelect.value = "butterchurn";
        const pending = fullscreen.updateVisualization();
        if (close) {
            dialog.close();
        } else {
            fullscreen.visualizationSelect.value = "waveform";
            await fullscreen.updateVisualization();
        }
        const instance = effect();
        resolve(instance);
        await pending;
        assert.equal(instance.disposed, true);
        assert.equal(fullscreen.visualization, null);
        assert.equal(overlays.at(-1).length, 1);
    });
}

test("load and render failures fall back without interrupting playback", async context => {
    const {fullscreen, overlays} = fixture(context);
    fullscreen.toggle();
    context.mock.method(fullscreen, "loadVisualization", async () => { throw new Error("No WebGL"); });
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    assert.equal(fullscreen.visualizationSelect.value, "waveform");
    assert.equal(fullscreen.visualizationStatus.textContent, "unavailable");
    assert.equal(overlays.at(-1).length, 1);

    let fail;
    const instance = effect();
    context.mock.method(fullscreen, "loadVisualization", async onError => { fail = onError; return instance; });
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    fail();
    assert.equal(instance.disposed, true);
    assert.equal(fullscreen.visualizationSelect.value, "waveform");
    assert.equal(fullscreen.visualizationStatus.textContent, "unavailable");
});

test("track changes randomize Butterchurn while repeated metadata updates keep the current effect", async context => {
    const {fullscreen, dialog} = fixture(context);
    const instance = effect();
    let selections = 0;
    instance.randomPreset = () => { selections += 1; };
    context.mock.method(fullscreen, "loadVisualization", async () => instance);
    fullscreen.toggle();
    fullscreen.visualizationSelect.value = "butterchurn";
    await fullscreen.updateVisualization();
    const notify = track => {
        const event = new Event("reinhardt:track");
        event.detail = track;
        document.dispatchEvent(event);
    };
    notify({url: "/stream/1/", position: "1"});
    assert.equal(selections, 1);
    notify({url: "/stream/1/", position: "1", title: "Updated title"});
    assert.equal(selections, 1);
    notify({url: "/stream/2/", position: "2"});
    assert.equal(selections, 2);
    notify({url: "/stream/2/", position: "3"});
    assert.equal(selections, 3);
    notify({});
    assert.equal(selections, 3);
    dialog.close();
    notify({url: "/stream/3/", position: "4"});
    assert.equal(selections, 3);
    assert.equal(instance.disposed, true);
});

test("fullscreen controls hide only after ten seconds of inactivity", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    context.mock.timers.tick(9999);
    assert.equal(dialog.dataset.controlsHidden, undefined);
    context.mock.timers.tick(1);
    assert.equal(dialog.dataset.controlsHidden, "true");
    assert.equal(dialog.open, true);
});

test("pointer, touch, scrolling, focus, and keyboard activity reveal controls and reset the delay", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    for (const type of ["pointermove", "pointerdown", "wheel", "scroll", "focusin", "keydown"]) {
        context.mock.timers.tick(10000);
        assert.equal(dialog.dataset.controlsHidden, "true");
        const target = type === "keydown" ? document : dialog;
        target.dispatchEvent(new Event(type));
        assert.equal(dialog.dataset.controlsHidden, undefined, type);
        context.mock.timers.tick(9999);
        assert.equal(dialog.dataset.controlsHidden, undefined, type);
        target.dispatchEvent(new Event(type));
        context.mock.timers.tick(1);
        assert.equal(dialog.dataset.controlsHidden, undefined, type);
    }
});

test("track updates do not count as user activity", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    context.mock.timers.tick(10000);
    fullscreen.onTrack({detail: {url: "/stream/2/", position: "2"}});
    assert.equal(dialog.dataset.controlsHidden, "true");
});

test("a tap on hidden controls only wakes them and the next tap can activate them", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    context.mock.timers.tick(10000);
    dialog.dispatchEvent(new Event("pointerdown"));
    const wakeClick = new Event("click", {cancelable: true});
    dialog.dispatchEvent(wakeClick);
    assert.equal(dialog.dataset.controlsHidden, undefined);
    assert.equal(wakeClick.defaultPrevented, true);
    dialog.dispatchEvent(new Event("pointerdown"));
    const nextClick = new Event("click", {cancelable: true});
    dialog.dispatchEvent(nextClick);
    assert.equal(nextClick.defaultPrevented, false);
});

test("a wake gesture without a click does not consume later keyboard or pointer activation", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    for (const type of ["keydown", "pointerdown"]) {
        context.mock.timers.tick(10000);
        dialog.dispatchEvent(new Event("pointerdown"));
        (type === "keydown" ? document : dialog).dispatchEvent(new Event(type));
        const click = new Event("click", {cancelable: true});
        dialog.dispatchEvent(click);
        assert.equal(click.defaultPrevented, false, type);
    }
});

test("Tab restores focus to the first or last control after they hide", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    for (const shiftKey of [false, true]) {
        context.mock.timers.tick(10000);
        const event = new Event("keydown", {cancelable: true});
        event.key = "Tab";
        event.shiftKey = shiftKey;
        document.dispatchEvent(event);
        const selector = shiftKey ? ".fullscreen-player__next" : ".fullscreen-player__close";
        assert.equal(document.activeElement, dialog.querySelector(selector));
        assert.equal(event.defaultPrevented, true);
        assert.equal(dialog.dataset.controlsHidden, undefined);
    }
});

test("closing clears the inactivity timer and reopening starts with visible controls", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    context.mock.timers.tick(9000);
    dialog.close();
    assert.equal(fullscreen.controlsTimer, null);
    context.mock.timers.tick(10000);
    assert.equal(dialog.dataset.controlsHidden, undefined);
    fullscreen.toggle();
    context.mock.timers.tick(9999);
    assert.equal(dialog.dataset.controlsHidden, undefined);
    context.mock.timers.tick(1);
    assert.equal(dialog.dataset.controlsHidden, "true");
});

test("disconnect removes activity listeners and cancels delayed hiding", context => {
    const {fullscreen, dialog} = fixture(context);
    fullscreen.toggle();
    fullscreen.disconnect();
    assert.equal(fullscreen.controlsTimer, null);
    context.mock.timers.tick(10000);
    assert.equal(dialog.dataset.controlsHidden, undefined);
    dialog.open = true;
    dialog.dataset.controlsHidden = "true";
    dialog.dispatchEvent(new Event("pointermove"));
    document.dispatchEvent(new Event("keydown"));
    assert.equal(dialog.dataset.controlsHidden, "true");
});
