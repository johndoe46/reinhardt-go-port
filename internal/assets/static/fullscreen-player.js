export class FullscreenPlayer {
    constructor(player) {
        this.player = player;
        this.dialog = document.getElementById("fullscreen-player");
        this.openButton = document.getElementById("player-expand");
        this.controlsTimer = null;
        this.ignoreControlClick = false;
        this.visualizationRequest = 0;
        this.loadVisualization = async (onError) => {
            const {createButterchurn} = await import("./butterchurn-visualization.js");
            return createButterchurn(onError);
        };
        this.onOpen = () => this.toggle();
        this.onClose = () => this.dialog.close();
        this.onClosed = () => {
            if (this.dialog.open) {
                return;
            }
            document.body.classList.remove("player-overlay-open");
            this.clearControlsTimer();
            this.ignoreControlClick = false;
            delete this.dialog.dataset.controlsHidden;
            this.visualizationRequest += 1;
            this.player.oscilloscope.setOverlay(null);
            this.disposeVisualization();
            if (this.previousFocus?.isConnected) {
                this.previousFocus.focus({preventScroll: true});
            }
        };
        this.onNext = () => this.player.onNextButtonClick();
        this.onTrack = (event) => {
            const track = event.detail;
            if (track.url && (track.url !== this.trackURL || track.position !== this.trackPosition)) {
                this.visualization?.randomPreset();
            }
            this.trackURL = track.url;
            this.trackPosition = track.position;
            this.updateTrack(track);
        };
        this.onVisualization = () => this.updateVisualization();
        this.onEffect = () => this.visualization?.nextPreset();
        this.onActivity = (event) => {
            const hidden = this.dialog.dataset.controlsHidden;
            if (event.type === "pointerdown") {
                this.ignoreControlClick = Boolean(hidden);
            } else if (event.type === "keydown") {
                this.ignoreControlClick = false;
            }
            this.showControls();
            if (hidden && event.type === "keydown" && event.key === "Tab") {
                event.preventDefault();
                const selector = event.shiftKey ? ".fullscreen-player__next" : ".fullscreen-player__close";
                this.dialog.querySelector(selector).focus({preventScroll: true});
            }
        };
        this.onControlsClick = (event) => {
            // Touch browsers can target the synthetic click after controls reappear.
            // The gesture that wakes them should never activate a hidden button.
            if (this.ignoreControlClick) {
                this.ignoreControlClick = false;
                event.preventDefault();
                event.stopImmediatePropagation();
            }
        };
    }

    connect() {
        if (!this.dialog) {
            return;
        }
        this.openButton?.addEventListener("click", this.onOpen);
        this.dialog.querySelector(".fullscreen-player__close").addEventListener("click", this.onClose);
        this.dialog.querySelector(".fullscreen-player__next").addEventListener("click", this.onNext);
        this.visualizationSelect = this.dialog.querySelector("#fullscreen-visualization");
        this.effectButton = this.dialog.querySelector(".fullscreen-player__effect");
        this.visualizationStatus = this.dialog.querySelector(".fullscreen-player__status");
        this.visualizationSelect.addEventListener("change", this.onVisualization);
        this.effectButton.addEventListener("click", this.onEffect);
        this.dialog.addEventListener("close", this.onClosed);
        this.dialog.addEventListener("click", this.onControlsClick, {capture: true});
        for (const type of ["pointermove", "pointerdown", "wheel", "scroll", "focusin"]) {
            this.dialog.addEventListener(type, this.onActivity, {passive: true});
        }
        document.addEventListener("keydown", this.onActivity, {capture: true});
        document.addEventListener("reinhardt:track", this.onTrack);
        const track = document.getElementById("now_playing")?.dataset || {};
        this.trackURL = track.url;
        this.trackPosition = track.position;
        this.updateTrack(track);
    }

    disconnect() {
        if (!this.dialog) {
            return;
        }
        if (this.dialog.open) {
            this.dialog.close();
            this.onClosed();
        }
        this.clearControlsTimer();
        this.openButton?.removeEventListener("click", this.onOpen);
        this.dialog.querySelector(".fullscreen-player__close").removeEventListener("click", this.onClose);
        this.dialog.querySelector(".fullscreen-player__next").removeEventListener("click", this.onNext);
        this.visualizationSelect.removeEventListener("change", this.onVisualization);
        this.effectButton.removeEventListener("click", this.onEffect);
        this.dialog.removeEventListener("close", this.onClosed);
        this.dialog.removeEventListener("click", this.onControlsClick, {capture: true});
        for (const type of ["pointermove", "pointerdown", "wheel", "scroll", "focusin"]) {
            this.dialog.removeEventListener(type, this.onActivity);
        }
        document.removeEventListener("keydown", this.onActivity, {capture: true});
        document.removeEventListener("reinhardt:track", this.onTrack);
    }

    toggle() {
        if (!this.dialog) {
            return;
        }
        if (this.dialog.open) {
            this.dialog.close();
            return;
        }
        this.previousFocus = document.activeElement;
        this.dialog.showModal();
        document.body.classList.add("player-overlay-open");
        this.showControls();
        this.updateVisualization();
    }

    clearControlsTimer() {
        clearTimeout(this.controlsTimer);
        this.controlsTimer = null;
    }

    showControls() {
        if (!this.dialog.open) {
            return;
        }
        this.clearControlsTimer();
        delete this.dialog.dataset.controlsHidden;
        this.controlsTimer = setTimeout(() => {
            this.controlsTimer = null;
            this.dialog.dataset.controlsHidden = "true";
        }, 10000);
    }

    disposeVisualization() {
        this.visualization?.dispose();
        this.visualization = null;
        this.effectButton.hidden = true;
        this.visualizationStatus.textContent = "";
        delete this.dialog.dataset.visualization;
    }

    async updateVisualization() {
        const request = ++this.visualizationRequest;
        const canvas = this.dialog.querySelector("canvas");
        this.player.oscilloscope.setOverlay(this.dialog.open ? canvas : null);
        this.disposeVisualization();
        if (!this.dialog.open || this.visualizationSelect.value !== "butterchurn") {
            return;
        }
        const isCurrent = () => request === this.visualizationRequest && this.dialog.open;
        const onError = () => {
            if (isCurrent()) {
                this.visualizationSelect.value = "waveform";
                this.updateVisualization();
                this.visualizationStatus.textContent = this.visualizationStatus.dataset.unavailable;
            }
        };
        this.visualizationStatus.textContent = this.visualizationStatus.dataset.loading;
        try {
            const visualization = await this.loadVisualization(onError);
            // A close or a newer selection can overtake the lazy module download.
            if (!isCurrent()) {
                visualization.dispose();
                return;
            }
            this.visualization = visualization;
            this.dialog.dataset.visualization = "butterchurn";
            this.player.oscilloscope.setOverlay(canvas, visualization);
            this.effectButton.hidden = false;
            this.visualizationStatus.textContent = "";
        } catch {
            onError();
        }
    }

    updateTrack(track) {
        const title = this.dialog.querySelector(".fullscreen-player__title");
        this.emptyTitle ||= title.textContent;
        title.textContent = track.title || this.emptyTitle;
        this.dialog.querySelector(".fullscreen-player__artist").textContent = track.artist || "";
        this.dialog.querySelector(".fullscreen-player__album").textContent = track.album || "";
        const artwork = this.dialog.querySelector("img");
        artwork.onerror = () => { artwork.hidden = true; };
        if (track.artwork512) {
            const nextSource = new URL(track.artwork512, window.location.href).href;
            if (artwork.src !== nextSource) {
                artwork.hidden = true;
                artwork.onload = () => { artwork.hidden = false; };
                artwork.src = nextSource;
            }
        } else {
            artwork.hidden = true;
            artwork.removeAttribute("src");
        }
    }
}
