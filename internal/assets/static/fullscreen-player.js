export class FullscreenPlayer {
    constructor(player) {
        this.player = player;
        this.dialog = document.getElementById("fullscreen-player");
        this.openButton = document.getElementById("player-expand");
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
        this.openButton?.removeEventListener("click", this.onOpen);
        this.dialog.querySelector(".fullscreen-player__close").removeEventListener("click", this.onClose);
        this.dialog.querySelector(".fullscreen-player__next").removeEventListener("click", this.onNext);
        this.visualizationSelect.removeEventListener("change", this.onVisualization);
        this.effectButton.removeEventListener("click", this.onEffect);
        this.dialog.removeEventListener("close", this.onClosed);
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
        this.updateVisualization();
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
