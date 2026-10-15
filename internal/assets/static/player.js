import {FooterOscilloscope} from "./oscilloscope.js";
import {FullscreenPlayer} from "./fullscreen-player.js";

class MAudio extends HTMLElement {
    constructor() {
        super();
        this.rendered = false;
        this.audioEl = null;
        this.playButtonEl = null;
        this.playIconEl = null;
        this.nextButtonEl = null;
        this.progressBarEl = null;
        this.progressEl = null;
        this.elapsedEl = null;
        this.totalEl = null;
        this.titleEl = null;
        this.eventsBound = false;
        this.playbackRequested = false;
        this.recoveryAttempts = 0;
        this.recoveryTimer = null;
        this.recoveryGeneration = 0;
        this.lastPlaybackTime = 0;
        this.progressFrame = null;
        this.onProgressFrame = () => {
            this.progressFrame = null;
            this.updateProgressUI();
            this.startProgressAnimation();
        };
        this.onProgressVisibilityChange = () => {
            this.stopProgressAnimation();
            this.updateTimeUI();
            this.startProgressAnimation();
        };
        this.onSeeking = () => {
            this.stopProgressAnimation();
            this.updateTimeUI();
        };
        this.onSeeked = () => {
            this.updateTimeUI();
            this.startProgressAnimation();
        };

        this.onPlay = () => {
            this.updatePlayIcon(true);
        };
        this.onPlaying = () => {
            this.clearRecovery();
            this.oscilloscope?.start();
            this.startProgressAnimation();
        };
        this.onPause = () => {
            this.stopProgressAnimation();
            this.updateTimeUI();
            this.oscilloscope?.stop();
            this.updatePlayIcon(false);
            this.scheduleRecovery();
        };
        this.onEnded = () => {
            this.stopProgressAnimation();
            this.oscilloscope?.stop();
            this.playbackRequested = false;
            this.clearRecovery();
            this.updatePlayIcon(false);
            this.updateTimeUI();
            if (typeof playNext === "function") {
                playNext();
            }
        };
        this.onTimeLikeUpdate = () => {
            this.updateTimeUI();
        };
        this.onTimeUpdate = () => {
            this.updateTimeUI();
            this.startProgressAnimation();
            if (this.audioEl && this.audioEl.currentTime > this.lastPlaybackTime + 0.25) {
                this.lastPlaybackTime = this.audioEl.currentTime;
                this.clearRecovery();
                this.recoveryAttempts = 0;
            }
        };
        this.onPlaybackInterrupted = () => {
            this.stopProgressAnimation();
            this.updateTimeUI();
            this.oscilloscope?.stop();
            this.scheduleRecovery();
        };
        this.onEnvironmentResume = () => {
            this.startProgressAnimation();
            if (document.visibilityState === "hidden" || !this.audioEl || !this.playbackRequested) {
                return;
            }
            if (!this.audioEl.paused && !this.audioEl.error && this.audioEl.readyState >= 3) {
                return;
            }
            this.recoveryAttempts = 0;
            this.scheduleRecovery(0);
        };
        this.onProgressClick = (evt) => {
            this.seekFromPointerEvent(evt);
        };
        this.onProgressKeyDown = (evt) => {
            this.seekFromKeyboardEvent(evt);
        };
        this.onPlayButtonClick = () => {
            this.togglePlay();
        };
        this.onNextButtonClick = () => {
            if (typeof playNext === "function") {
                playNext();
            }
        };
    }

    get src() {
        return this.getAttribute("src");
    }

    set src(val) {
        this.setAttribute("src", val);
    }


    static get observedAttributes() {
        return ["src", "data-oscilloscope"];
    }

    togglePlay() {
        if (!this.audioEl) {
            return;
        }
        if (this.audioEl.paused) {
            this.play();
        } else {
            this.pause();
        }
    }

    pause() {
        this.playbackRequested = false;
        this.clearRecovery();
        if (this.audioEl) {
            this.audioEl.pause();
        }
    }

    play() {
        if (!this.audioEl) {
            return;
        }
        this.playbackRequested = true;
        this.attemptPlayback();
    }

    attemptPlayback() {
        if (!this.audioEl || !this.playbackRequested || !this.audioEl.getAttribute("src")) {
            return;
        }
        this.oscilloscope?.prepare();
        const playPromise = this.audioEl.play();
        if (playPromise) {
            playPromise.catch((error) => {
                // Browsers require a user gesture for the first play; retries cannot fix that.
                if (error.name !== "NotAllowedError") {
                    this.scheduleRecovery();
                }
            });
        }
    }

    clearRecovery() {
        if (this.recoveryTimer !== null) {
            window.clearTimeout(this.recoveryTimer);
            this.recoveryTimer = null;
        }
    }

    scheduleRecovery(delay = null) {
        if (!this.audioEl || !this.playbackRequested || this.audioEl.ended || this.recoveryTimer !== null) {
            return;
        }
        const retryDelay = delay === null
            ? Math.min(30000, 2000 * (2 ** this.recoveryAttempts))
            : delay;
        this.recoveryTimer = window.setTimeout(() => {
            this.recoveryTimer = null;
            this.recoverPlayback();
        }, retryDelay);
    }

    recoverPlayback() {
        if (!this.audioEl || !this.playbackRequested || this.audioEl.ended) {
            return;
        }
        if (navigator.onLine === false) {
            this.scheduleRecovery(5000);
            return;
        }

        const resumeAt = this.audioEl.currentTime;
        const generation = ++this.recoveryGeneration;
        this.recoveryAttempts += 1;
        this.audioEl.load();
        this.audioEl.addEventListener("loadedmetadata", () => {
            if (!this.audioEl || generation !== this.recoveryGeneration || !this.playbackRequested) {
                return;
            }
            if (resumeAt > 0 && Number.isFinite(this.audioEl.duration)) {
                this.audioEl.currentTime = Math.min(resumeAt, this.audioEl.duration);
            }
            this.attemptPlayback();
        }, {once: true});
        this.scheduleRecovery();
    }

    updatePlayIcon(isPlaying) {
        if (!this.playIconEl) {
            return;
        }
        this.playIconEl.classList.toggle("bi-play-btn", !isPlaying);
        this.playIconEl.classList.toggle("bi-pause-btn", isPlaying);
        if (this.playButtonEl) {
            this.playButtonEl.setAttribute("aria-pressed", isPlaying ? "true" : "false");
        }
    }

    formatSecondsAsTime(secs) {
        const safeSecs = Number.isFinite(secs) && secs > 0 ? secs : 0;
        let hr = Math.floor(safeSecs / 3600);
        let min = Math.floor((safeSecs - (hr * 3600)) / 60);
        let sec = Math.floor(safeSecs - (hr * 3600) - (min * 60));
        if (hr < 10) {
            hr = "0" + hr;
        }
        if (min < 10) {
            min = "0" + min;
        }
        if (sec < 10) {
            sec = "0" + sec;
        }
        if (hr === "00") {
            return min + ":" + sec;
        }
        return hr + ":" + min + ":" + sec;
    }

    getProgressPercent() {
        if (!this.audioEl || !Number.isFinite(this.audioEl.duration) || this.audioEl.duration <= 0) {
            return 0;
        }
        const raw = (this.audioEl.currentTime / this.audioEl.duration) * 100;
        return Math.min(100, Math.max(0, raw));
    }

    startProgressAnimation() {
        if (this.progressFrame !== null || !this.progressEl || !this.audioEl || !this.isConnected
            || this.audioEl.paused || this.audioEl.ended || this.audioEl.seeking || this.audioEl.error
            || this.audioEl.readyState < 3 || document.visibilityState === "hidden") {
            return;
        }
        // Read the media clock each frame: buffering, seeking and playback speed
        // stay accurate without predicting progress or delaying it with a transition.
        this.progressFrame = window.requestAnimationFrame(this.onProgressFrame);
    }

    stopProgressAnimation() {
        if (this.progressFrame !== null) {
            window.cancelAnimationFrame(this.progressFrame);
            this.progressFrame = null;
        }
    }

    updateProgressUI() {
        if (!this.audioEl || !this.progressEl || !this.progressBarEl) {
            return;
        }
        const percent = this.getProgressPercent();
        this.progressEl.style.width = percent + "%";
        const accessiblePercent = String(Math.floor(percent));
        if (this.progressBarEl.getAttribute("aria-valuenow") !== accessiblePercent) {
            this.progressBarEl.setAttribute("aria-valuenow", accessiblePercent);
        }
    }

    updateTimeUI() {
        if (!this.audioEl || !this.elapsedEl || !this.totalEl || !this.progressEl || !this.progressBarEl) {
            return;
        }
        const elapsed = Number.isFinite(this.audioEl.currentTime) ? this.audioEl.currentTime : 0;
        const duration = Number.isFinite(this.audioEl.duration) ? this.audioEl.duration : 0;
        this.elapsedEl.textContent = this.formatSecondsAsTime(elapsed);
        this.totalEl.textContent = this.formatSecondsAsTime(duration);
        this.updateProgressUI();
    }

    /**
     *
     * @param {number} offsetPercent
     */
    skipTo(offsetPercent) {
        if (!this.audioEl || !Number.isFinite(this.audioEl.duration) || this.audioEl.duration <= 0) {
            return;
        }
        const clamped = Math.max(0, Math.min(100, Number(offsetPercent)));
        const skipTime = Math.floor((this.audioEl.duration * clamped) / 100);
        this.audioEl.currentTime = skipTime;
        this.updateTimeUI();
        this.play();
    }

    seekBySeconds(deltaSeconds) {
        if (!this.audioEl || !Number.isFinite(this.audioEl.duration) || this.audioEl.duration <= 0) {
            return;
        }
        const next = this.audioEl.currentTime + deltaSeconds;
        this.audioEl.currentTime = Math.max(0, Math.min(this.audioEl.duration, next));
        this.updateTimeUI();
    }

    seekFromPointerEvent(evt) {
        if (!this.progressBarEl) {
            return;
        }
        const rect = this.progressBarEl.getBoundingClientRect();
        if (rect.width <= 0) {
            return;
        }
        const offsetPx = evt.clientX - rect.left;
        const percent = (offsetPx / rect.width) * 100;
        this.skipTo(percent);
    }

    seekFromKeyboardEvent(evt) {
        switch (evt.key) {
            case "ArrowLeft":
                evt.preventDefault();
                this.seekBySeconds(-5);
                break;
            case "ArrowRight":
                evt.preventDefault();
                this.seekBySeconds(5);
                break;
            case "Home":
                evt.preventDefault();
                this.skipTo(0);
                break;
            case "End":
                evt.preventDefault();
                this.skipTo(100);
                break;
            default:
                break;
        }
    }

    bindAudioEvents() {
        if (this.eventsBound || !this.audioEl || !this.playButtonEl || !this.progressBarEl || !this.nextButtonEl) {
            return;
        }
        this.eventsBound = true;
        this.oscilloscope?.connect();
        this.audioEl.addEventListener("play", this.onPlay);
        this.audioEl.addEventListener("playing", this.onPlaying);
        this.audioEl.addEventListener("pause", this.onPause);
        this.audioEl.addEventListener("ended", this.onEnded);
        this.audioEl.addEventListener("timeupdate", this.onTimeUpdate);
        this.audioEl.addEventListener("loadedmetadata", this.onTimeLikeUpdate);
        this.audioEl.addEventListener("durationchange", this.onTimeLikeUpdate);
        this.audioEl.addEventListener("seeking", this.onSeeking);
        this.audioEl.addEventListener("seeked", this.onSeeked);
        this.audioEl.addEventListener("waiting", this.onPlaybackInterrupted);
        this.audioEl.addEventListener("stalled", this.onPlaybackInterrupted);
        this.audioEl.addEventListener("error", this.onPlaybackInterrupted);
        this.playButtonEl.addEventListener("click", this.onPlayButtonClick);
        this.nextButtonEl.addEventListener("click", this.onNextButtonClick);
        this.progressBarEl.addEventListener("click", this.onProgressClick);
        this.progressBarEl.addEventListener("keydown", this.onProgressKeyDown);
        document.addEventListener("visibilitychange", this.onEnvironmentResume);
        document.addEventListener("visibilitychange", this.onProgressVisibilityChange);
        window.addEventListener("pageshow", this.onEnvironmentResume);
        window.addEventListener("online", this.onEnvironmentResume);
        this.startProgressAnimation();
    }

    unbindAudioEvents() {
        if (!this.eventsBound || !this.audioEl || !this.playButtonEl || !this.progressBarEl || !this.nextButtonEl) {
            return;
        }
        this.eventsBound = false;
        this.stopProgressAnimation();
        this.oscilloscope?.disconnect();
        this.audioEl.removeEventListener("play", this.onPlay);
        this.audioEl.removeEventListener("playing", this.onPlaying);
        this.audioEl.removeEventListener("pause", this.onPause);
        this.audioEl.removeEventListener("ended", this.onEnded);
        this.audioEl.removeEventListener("timeupdate", this.onTimeUpdate);
        this.audioEl.removeEventListener("loadedmetadata", this.onTimeLikeUpdate);
        this.audioEl.removeEventListener("durationchange", this.onTimeLikeUpdate);
        this.audioEl.removeEventListener("seeking", this.onSeeking);
        this.audioEl.removeEventListener("seeked", this.onSeeked);
        this.audioEl.removeEventListener("waiting", this.onPlaybackInterrupted);
        this.audioEl.removeEventListener("stalled", this.onPlaybackInterrupted);
        this.audioEl.removeEventListener("error", this.onPlaybackInterrupted);
        this.playButtonEl.removeEventListener("click", this.onPlayButtonClick);
        this.nextButtonEl.removeEventListener("click", this.onNextButtonClick);
        this.progressBarEl.removeEventListener("click", this.onProgressClick);
        this.progressBarEl.removeEventListener("keydown", this.onProgressKeyDown);
        document.removeEventListener("visibilitychange", this.onEnvironmentResume);
        document.removeEventListener("visibilitychange", this.onProgressVisibilityChange);
        window.removeEventListener("pageshow", this.onEnvironmentResume);
        window.removeEventListener("online", this.onEnvironmentResume);
    }

    render() {
        this.innerHTML = `
            <table style="width:100%;table-layout:fixed;margin:0;padding:0;">
                <tr>
                    <td style="width:1.5em;margin:0;padding:0;">
                        <button class="m-audio-play-button btn"
                                aria-label="Play/Pause"
                                aria-pressed="false"
                                style="margin-left:0;padding-left:0;margin-right:0;padding-right:0;text-decoration:none;">
                            <i class="bi bi-play-btn"></i>
                        </button>
                    </td>
                    <td>
                        <div class="progress m-audio-progress-bar"
                             role="slider"
                             aria-label="Seek audio"
                             aria-valuemin="0"
                             aria-valuemax="100"
                             aria-valuenow="0"
                             tabindex="0"
                             style=" position:relative; height:1em;cursor: pointer;">
                            <div class="progress-bar m-audio-progress" style="width: 0%;transition: none;">
                            </div>
                            <span style="cursor:auto;display: block;position:absolute;right:0.5em;top:-0.1em;margin: 0;padding: 0; text-align: right;font-family: monospace;font-size: 0.8em;">
                                <span style="margin: 0;padding: 0" class="m-audio-time-elapsed">00:00</span>/<span style="margin: 0;padding: 0" class="m-audio-time-total">00:00</span>
                            </span>
                        </div>
                        <span class="m-audio-title small" style="display:none;"></span>
                    </td>
                    <td style="width:1.5em;margin:0;padding:0;text-align:right;">
                        <button class="m-audio-next-button btn"
                                aria-label="Next"
                                style="margin-left:0;padding-left:0;margin-right:0;padding-right:0;text-decoration:none;">
                            <i class="bi bi-skip-end-btn"></i>
                        </button>
                    </td>
                </tr>
            </table>
            <audio preload="auto"></audio>
        `;

        this.audioEl = this.querySelector("audio");
        this.playButtonEl = this.querySelector(".m-audio-play-button");
        this.playIconEl = this.querySelector(".m-audio-play-button i.bi");
        this.nextButtonEl = this.querySelector(".m-audio-next-button");
        this.progressBarEl = this.querySelector(".m-audio-progress-bar");
        this.progressEl = this.querySelector(".m-audio-progress");
        this.elapsedEl = this.querySelector(".m-audio-time-elapsed");
        this.totalEl = this.querySelector(".m-audio-time-total");
        this.titleEl = this.querySelector(".m-audio-title");

        this.oscilloscope = new FooterOscilloscope(this);
        this.audioEl.setAttribute("src", this.src || "");
        this.titleEl.textContent = this.title || "";
        this.bindAudioEvents();
        this.updateTimeUI();
    }

    attributeChangedCallback(name, oldValue, newValue) {
        if (name === "data-oscilloscope" && oldValue !== newValue) {
            this.oscilloscope?.setEnabled(newValue !== "false");
        }
        if (name === "src" && oldValue !== newValue) {
            let audio = this.audioEl || this.querySelector("audio");
            if (!audio) {
                return;
            }
            const shouldContinue = this.playbackRequested || (!audio.paused && !audio.ended);
            this.clearRecovery();
            this.recoveryGeneration += 1;
            this.recoveryAttempts = 0;
            this.lastPlaybackTime = 0;
            this.stopProgressAnimation();
            this.oscilloscope?.stop();
            if (this.oscilloscope?.source && !this.oscilloscope.canAnalyse(newValue)) {
                // Web Audio silences remote media without CORS. A fresh native
                // element preserves delegated streaming without requiring CORS.
                this.unbindAudioEvents();
                audio.pause();
                const replacement = audio.cloneNode(false);
                audio.replaceWith(replacement);
                audio = this.audioEl = replacement;
                this.oscilloscope.releaseAudio();
                this.bindAudioEvents();
            }
            audio.setAttribute("src", newValue || "");
            audio.load();
            this.updateTimeUI();
            if (shouldContinue) {
                this.play();
            }
        }
    }

    connectedCallback() {
        if (!this.rendered) {
            this.render();
            this.rendered = true;
        } else {
            this.bindAudioEvents();
        }
        this.fullscreen ||= new FullscreenPlayer(this);
        this.fullscreen.connect();
    }

    disconnectedCallback() {
        this.fullscreen?.disconnect();
        this.stopProgressAnimation();
        this.clearRecovery();
        this.unbindAudioEvents();
    }
}


customElements.define("m-audio", MAudio);
