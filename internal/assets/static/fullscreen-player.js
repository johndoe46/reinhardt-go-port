export class FullscreenPlayer {
    constructor(player) {
        this.player = player;
        this.dialog = document.getElementById("fullscreen-player");
        this.openButton = document.getElementById("player-expand");
        this.onOpen = () => this.toggle();
        this.onClose = () => this.dialog.close();
        this.onClosed = () => {
            if (this.dialog.open) {
                return;
            }
            document.body.classList.remove("player-overlay-open");
            this.player.oscilloscope.setOverlay(null);
            if (this.previousFocus?.isConnected) {
                this.previousFocus.focus({preventScroll: true});
            }
        };
        this.onNext = () => this.player.onNextButtonClick();
        this.onTrack = (event) => this.updateTrack(event.detail);
    }

    connect() {
        if (!this.dialog) {
            return;
        }
        this.openButton?.addEventListener("click", this.onOpen);
        this.dialog.querySelector(".fullscreen-player__close").addEventListener("click", this.onClose);
        this.dialog.querySelector(".fullscreen-player__next").addEventListener("click", this.onNext);
        this.dialog.addEventListener("close", this.onClosed);
        document.addEventListener("reinhardt:track", this.onTrack);
        this.updateTrack(document.getElementById("now_playing")?.dataset || {});
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
        this.player.oscilloscope.setOverlay(this.dialog.querySelector("canvas"));
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
