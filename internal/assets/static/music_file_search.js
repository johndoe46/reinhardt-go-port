(function () {
    const app = document.getElementById("music-file-search-app");
    if (!app) {
        return;
    }

    const form = document.getElementById("music-file-search-form");
    const resultsElement = document.getElementById("music-file-search-results");
    const errorElement = document.getElementById("music-file-search-error");
    const spinnerElement = document.getElementById("music-file-search-spinner");
    const pageLabelElement = document.getElementById("music-file-search-page-label");
    const prevButton = document.getElementById("music-file-search-prev");
    const nextButton = document.getElementById("music-file-search-next");
    const sortButtons = Array.from(document.querySelectorAll("[data-order-by]"));
    const crossfaderElement = document.getElementById("dj-crossfader");
    const crossfaderReadoutElement = document.getElementById("dj-crossfader-readout");
    const autoDjToggleButton = document.getElementById("dj-auto-mode-toggle");
    const autoCrossfadeAButton = document.getElementById("dj-auto-crossfade-a");
    const autoCrossfadeBButton = document.getElementById("dj-auto-crossfade-b");
    const meterAElement = document.getElementById("dj-meter-a");
    const meterBElement = document.getElementById("dj-meter-b");
    const pageSize = 50;
    const autoCrossfadeDurationMs = 10000;
    const autoCrossfadeStepMs = 50;
    const normalizedPlaybackHeadroom = 0.5;
    const deckColors = {A: "primary", B: "danger"};
    let audioContext = null;
    let autoCrossfadeTimer = null;
    let autoCrossfadeStartedAt = 0;
    let meterAnimationFrame = null;

    let currentOffset = 0;
    let totalCount = 0;
    let requestCounter = 0;
    let currentOrderBy = "";
    let autoDjEnabled = true;
    let autoDjTransition = null;
    let autoDjQueueTrackId = null;
    let autoDjSyncInFlight = false;
    let autoDjSyncRetryAt = 0;
    const autoDjFetchInFlight = {A: false, B: false};
    const autoDjRetryAt = {A: 0, B: 0};

    const decks = buildDeckRegistry();

    function buildDeckRegistry() {
        const registry = {};
        for (const deckElement of Array.from(document.querySelectorAll("[data-deck]"))) {
            const deckName = deckElement.dataset.deck;
            const audio = deckElement.querySelector('[data-role="audio"]');
            registry[deckName] = {
                name: deckName,
                element: deckElement,
                audio: audio,
                title: deckElement.querySelector('[data-role="title"]'),
                meta: deckElement.querySelector('[data-role="meta"]'),
                waveform: deckElement.querySelector('[data-role="waveform"]'),
                progressTrack: deckElement.querySelector('[data-role="progress-track"]'),
                progress: deckElement.querySelector('[data-role="progress"]'),
                elapsed: deckElement.querySelector('[data-role="elapsed"]'),
                duration: deckElement.querySelector('[data-role="duration"]'),
                playButton: deckElement.querySelector('[data-role="play"]'),
                restartButton: deckElement.querySelector('[data-role="restart"]'),
                stopButton: deckElement.querySelector('[data-role="stop"]'),
                track: null,
                normalizationGain: 1,
                isScrubbing: false,
                waveformRequestId: 0,
                waveformPeaks: null,
                meterPeaks: null,
                meterDisplayLevel: 0,
                outputGain: 0,
                waveformPlaceholderLabel: "No track loaded",
                cueInTime: 0,
                outroStartTime: null,
            };
        }
        return registry;
    }

    function getDeckWaveformContext(deck) {
        return deck.waveform.getContext("2d");
    }

    function getWaveformThemeColors(deck) {
        const styles = getComputedStyle(document.documentElement);
        return {
            background: styles.getPropertyValue("--bs-tertiary-bg").trim() || "#f8f9fa",
            text: styles.getPropertyValue("--bs-secondary-color").trim() || "#6c757d",
            stroke: deck.name === "A"
                ? (styles.getPropertyValue("--bs-primary").trim() || "#0d6efd")
                : (styles.getPropertyValue("--bs-danger").trim() || "#dc3545"),
        };
    }

    function drawWaveformPlaceholder(deck, label) {
        const context = getDeckWaveformContext(deck);
        const {width, height} = deck.waveform;
        const colors = getWaveformThemeColors(deck);
        deck.waveformPeaks = null;
        deck.meterPeaks = null;
        deck.meterDisplayLevel = 0;
        deck.waveformPlaceholderLabel = label;
        context.clearRect(0, 0, width, height);
        context.fillStyle = colors.background;
        context.fillRect(0, 0, width, height);

        context.strokeStyle = colors.stroke;
        context.lineWidth = 1;
        context.beginPath();
        context.moveTo(0, height / 2);
        context.lineTo(width, height / 2);
        context.stroke();

        context.fillStyle = colors.text;
        context.font = "12px sans-serif";
        context.textAlign = "center";
        context.textBaseline = "middle";
        context.fillText(label, width / 2, height / 2 - 16);
    }

    function ensureAudioContext() {
        if (!audioContext) {
            const ContextClass = window.AudioContext || window.webkitAudioContext;
            if (!ContextClass) {
                return null;
            }
            audioContext = new ContextClass();
        }
        return audioContext;
    }

    function buildWaveformPeaks(channelData, width) {
        const step = Math.max(1, Math.floor(channelData.length / width));
        const peaks = [];

        for (let x = 0; x < width; x += 1) {
            const start = x * step;
            const end = Math.min(start + step, channelData.length);
            let min = 1;
            let max = -1;

            for (let index = start; index < end; index += 1) {
                const sample = channelData[index];
                if (sample < min) {
                    min = sample;
                }
                if (sample > max) {
                    max = sample;
                }
            }
            peaks.push([min, max]);
        }

        return peaks;
    }

    function buildMeterPeaks(channelData, resolution) {
        const step = Math.max(1, Math.floor(channelData.length / resolution));
        const peaks = [];

        for (let x = 0; x < resolution; x += 1) {
            const start = x * step;
            const end = Math.min(start + step, channelData.length);
            let localPeak = 0;

            for (let index = start; index < end; index += 1) {
                const absoluteSample = Math.abs(channelData[index]);
                if (absoluteSample > localPeak) {
                    localPeak = absoluteSample;
                }
            }
            peaks.push(localPeak);
        }

        return peaks;
    }

    function calculateNormalizationGain(audioBuffer) {
        const targetRms = 0.18;
        const targetPeak = 0.92;
        const sampleStride = 32;
        let peak = 0;
        let sumSquares = 0;
        let sampleCount = 0;

        for (let channelIndex = 0; channelIndex < audioBuffer.numberOfChannels; channelIndex += 1) {
            const channelData = audioBuffer.getChannelData(channelIndex);
            for (let sampleIndex = 0; sampleIndex < channelData.length; sampleIndex += sampleStride) {
                const sample = channelData[sampleIndex];
                const absoluteSample = Math.abs(sample);
                if (absoluteSample > peak) {
                    peak = absoluteSample;
                }
                sumSquares += sample * sample;
                sampleCount += 1;
            }
        }

        if (sampleCount === 0 || peak === 0) {
            return 1;
        }

        const rms = Math.sqrt(sumSquares / sampleCount);
        const rmsGain = rms > 0 ? targetRms / rms : 1;
        const peakGain = peak > 0 ? targetPeak / peak : 1;
        return clamp(Math.min(rmsGain, peakGain), 0.25, 2);
    }

    function smoothSeries(values, radius) {
        const smoothed = [];
        for (let index = 0; index < values.length; index += 1) {
            let sum = 0;
            let count = 0;
            const start = Math.max(0, index - radius);
            const end = Math.min(values.length - 1, index + radius);
            for (let cursor = start; cursor <= end; cursor += 1) {
                sum += values[cursor];
                count += 1;
            }
            smoothed.push(count > 0 ? sum / count : 0);
        }
        return smoothed;
    }

    function percentile(values, ratio) {
        if (!values.length) {
            return 0;
        }
        const sorted = Array.from(values).sort(function (left, right) {
            return left - right;
        });
        const index = clamp(
            Math.floor((sorted.length - 1) * ratio),
            0,
            sorted.length - 1
        );
        return sorted[index];
    }

    function buildEnergyEnvelope(channelData, sampleRate) {
        const slicesPerSecond = 4;
        const sampleStride = 8;
        const windowSize = Math.max(1, Math.floor(sampleRate / slicesPerSecond));
        const energies = [];

        for (let start = 0; start < channelData.length; start += windowSize) {
            const end = Math.min(start + windowSize, channelData.length);
            let sumSquares = 0;
            let sampleCount = 0;

            for (let index = start; index < end; index += sampleStride) {
                const sample = channelData[index];
                sumSquares += sample * sample;
                sampleCount += 1;
            }

            energies.push(sampleCount > 0 ? Math.sqrt(sumSquares / sampleCount) : 0);
        }

        return {
            slicesPerSecond: slicesPerSecond,
            energies: smoothSeries(energies, 2),
        };
    }

    function findFirstSustainedIndex(values, threshold, minRunLength) {
        let runLength = 0;
        let runStart = 0;

        for (let index = 0; index < values.length; index += 1) {
            if (values[index] >= threshold) {
                if (runLength === 0) {
                    runStart = index;
                }
                runLength += 1;
                if (runLength >= minRunLength) {
                    return runStart;
                }
            } else {
                runLength = 0;
            }
        }

        return null;
    }

    function findTrailingQuietStartIndex(values, threshold, minRunLength) {
        let runLength = 0;

        for (let index = values.length - 1; index >= 0; index -= 1) {
            if (values[index] <= threshold) {
                runLength += 1;
            } else {
                if (runLength >= minRunLength) {
                    return index + 1;
                }
                runLength = 0;
            }
        }

        if (runLength >= minRunLength) {
            return 0;
        }

        return null;
    }

    function analyzeCuePoints(audioBuffer) {
        const durationSeconds = Number.isFinite(audioBuffer.duration) ? audioBuffer.duration : 0;
        const defaultOutroStartTime = Math.max(0, durationSeconds - (autoCrossfadeDurationMs / 1000));
        if (durationSeconds <= 0 || audioBuffer.numberOfChannels === 0) {
            return {cueInTime: 0, outroStartTime: defaultOutroStartTime};
        }

        const {energies, slicesPerSecond} = buildEnergyEnvelope(
            audioBuffer.getChannelData(0),
            audioBuffer.sampleRate
        );
        if (energies.length === 0) {
            return {cueInTime: 0, outroStartTime: defaultOutroStartTime};
        }

        const maxEnergy = Math.max(...energies);
        const meanEnergy = energies.reduce((sum, value) => sum + value, 0) / energies.length;
        const upperQuartileEnergy = percentile(energies, 0.75);
        const maxCueInTime = Math.min(12, durationSeconds * 0.2);
        const introSearchLength = Math.max(
            1,
            Math.min(energies.length, Math.ceil(maxCueInTime * slicesPerSecond))
        );
        const introEnergies = energies.slice(0, introSearchLength);
        const activeThreshold = Math.max(
            maxEnergy * 0.16,
            upperQuartileEnergy * 0.45,
            meanEnergy * 1.1
        );
        const quietThreshold = Math.max(maxEnergy * 0.08, meanEnergy * 0.6);
        const minRunLength = Math.max(1, Math.round(slicesPerSecond * 0.75));
        const firstActiveIndex = findFirstSustainedIndex(
            introEnergies,
            activeThreshold,
            minRunLength
        );
        const trailingQuietStartIndex = findTrailingQuietStartIndex(
            energies,
            quietThreshold,
            minRunLength
        );

        const cueInTime = firstActiveIndex === null
            ? 0
            : Math.max(0, (firstActiveIndex / slicesPerSecond) - 0.25);
        const quietOutroStartTime = trailingQuietStartIndex === null
            ? defaultOutroStartTime
            : trailingQuietStartIndex / slicesPerSecond;
        return {
            cueInTime: clamp(cueInTime, 0, maxCueInTime),
            outroStartTime: clamp(
                Math.min(defaultOutroStartTime, quietOutroStartTime),
                0,
                durationSeconds
            ),
        };
    }

    function drawWaveform(deck) {
        if (!deck.waveformPeaks) {
            drawWaveformPlaceholder(deck, deck.waveformPlaceholderLabel || "Waveform unavailable");
            return;
        }
        const context = getDeckWaveformContext(deck);
        const {width, height} = deck.waveform;
        const middleY = height / 2;
        const colors = getWaveformThemeColors(deck);

        context.clearRect(0, 0, width, height);
        context.fillStyle = colors.background;
        context.fillRect(0, 0, width, height);

        context.strokeStyle = colors.stroke;
        context.lineWidth = 1.2;
        context.beginPath();

        for (let x = 0; x < deck.waveformPeaks.length; x += 1) {
            const [min, max] = deck.waveformPeaks[x];
            context.moveTo(x, middleY + min * middleY * 0.82);
            context.lineTo(x, middleY + max * middleY * 0.82);
        }

        context.stroke();
    }

    async function loadDeckWaveform(deck, url) {
        const context = ensureAudioContext();
        deck.waveformRequestId += 1;
        const requestId = deck.waveformRequestId;

        if (!context) {
            drawWaveformPlaceholder(deck, "Waveform unavailable");
            return;
        }

        drawWaveformPlaceholder(deck, "Loading waveform...");

        try {
            const response = await fetch(url, {credentials: "same-origin"});
            if (!response.ok) {
                throw new Error(`Waveform request failed with status ${response.status}.`);
            }

            const arrayBuffer = await response.arrayBuffer();
            const audioBuffer = await context.decodeAudioData(arrayBuffer.slice(0));
            if (requestId !== deck.waveformRequestId) {
                return;
            }

            deck.normalizationGain = calculateNormalizationGain(audioBuffer);
            const channelData = audioBuffer.getChannelData(0);
            const cuePoints = analyzeCuePoints(audioBuffer);
            deck.cueInTime = cuePoints.cueInTime;
            deck.outroStartTime = cuePoints.outroStartTime;
            deck.meterPeaks = buildMeterPeaks(
                channelData,
                Math.min(96000, Math.max(8192, Math.ceil(audioBuffer.duration * 1000)))
            );
            deck.meterDisplayLevel = 0;
            updateDeckVolumes();
            try {
                deck.waveformPeaks = buildWaveformPeaks(channelData, deck.waveform.width);
                deck.waveformPlaceholderLabel = "";
                drawWaveform(deck);
            } catch (error) {
                drawWaveformPlaceholder(deck, "Waveform unavailable");
            }
        } catch (error) {
            if (requestId !== deck.waveformRequestId) {
                return;
            }
            deck.normalizationGain = 1;
            deck.cueInTime = 0;
            deck.outroStartTime = null;
            drawWaveformPlaceholder(deck, "Waveform unavailable");
        }
    }

    function redrawWaveformsForTheme() {
        for (const deck of Object.values(decks)) {
            if (deck.waveformPeaks) {
                drawWaveform(deck);
            } else {
                drawWaveformPlaceholder(
                    deck,
                    deck.waveformPlaceholderLabel || "Waveform unavailable"
                );
            }
        }
    }

    function formatSecondsAsTime(secs) {
        const safeSecs = Number.isFinite(secs) && secs > 0 ? secs : 0;
        const minutes = Math.floor(safeSecs / 60);
        const seconds = Math.floor(safeSecs % 60);
        return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
    }

    function getTrimmedValue(value) {
        return typeof value === "string" ? value.trim() : value;
    }

    function getCsrfToken() {
        // POST fetches send this value in X-CSRFToken and the form body, while
        // the browser sends csrftoken automatically. The server requires a match.
        const tokenInput = form.querySelector('[name="csrfmiddlewaretoken"]');
        if (tokenInput && tokenInput.value) {
            return tokenInput.value;
        }
        // The CSRF cookie is readable by JavaScript (HttpOnly=false), providing
        // a fallback when the form has no token input.
        const cookiePrefix = "csrftoken=";
        for (const part of document.cookie.split(";")) {
            const cookie = part.trim();
            if (cookie.startsWith(cookiePrefix)) {
                return decodeURIComponent(cookie.slice(cookiePrefix.length));
            }
        }
        return "";
    }

    function clamp(value, min, max) {
        return Math.max(min, Math.min(max, value));
    }

    function getOtherDeckName(deckName) {
        return deckName === "A" ? "B" : "A";
    }

    function getCrossfaderPosition() {
        const rawValue = parseInt(crossfaderElement.value, 10) || 0;
        return clamp(rawValue / 100, -1, 1);
    }

    function stopAutoCrossfade() {
        if (autoCrossfadeTimer !== null) {
            clearTimeout(autoCrossfadeTimer);
            autoCrossfadeTimer = null;
        }
        autoCrossfadeStartedAt = 0;
    }

    function cancelAutoDjTransition() {
        stopAutoCrossfade();
        autoDjTransition = null;
    }

    function updateCrossfaderReadout() {
        if (!crossfaderReadoutElement) {
            return;
        }
        const position = getCrossfaderPosition();
        if (position === 0) {
            crossfaderReadoutElement.textContent = "Center";
        } else if (position < 0) {
            crossfaderReadoutElement.textContent = `A ${Math.abs(Math.round(position * 100))}%`;
        } else {
            crossfaderReadoutElement.textContent = `B ${Math.round(position * 100)}%`;
        }
    }

    function updateDeckVolumes() {
        const crossfaderPosition = getCrossfaderPosition();
        const crossfaderGains = {
            A: clamp(1 - crossfaderPosition, 0, 1),
            B: clamp(1 + crossfaderPosition, 0, 1),
        };

        for (const deckName of Object.keys(decks)) {
            const deck = decks[deckName];
            const nextVolume = clamp(
                crossfaderGains[deckName] * normalizedPlaybackHeadroom * deck.normalizationGain,
                0,
                1
            );
            deck.outputGain = nextVolume;
            deck.audio.volume = nextVolume;
        }
        updateCrossfaderReadout();
    }

    function getDeckMeterLevel(deck) {
        if (
            !deck.track ||
            deck.audio.paused ||
            !deck.meterPeaks ||
            !Number.isFinite(deck.audio.duration) ||
            deck.audio.duration <= 0
        ) {
            return 0;
        }

        const progress = clamp(deck.audio.currentTime / deck.audio.duration, 0, 1);
        const peakIndex = Math.min(
            deck.meterPeaks.length - 1,
            Math.max(0, Math.floor(progress * deck.meterPeaks.length))
        );
        const localAmplitude = deck.meterPeaks[peakIndex];
        return clamp(localAmplitude * deck.outputGain, 0, 1);
    }

    function updateMeterUI() {
        const decayPerFrame = 0.18;
        if (meterAElement) {
            const targetLevelA = getDeckMeterLevel(decks.A);
            decks.A.meterDisplayLevel = targetLevelA > decks.A.meterDisplayLevel
                ? targetLevelA
                : Math.max(targetLevelA, decks.A.meterDisplayLevel - decayPerFrame);
            meterAElement.style.height = `${Math.round(decks.A.meterDisplayLevel * 100)}%`;
        }
        if (meterBElement) {
            const targetLevelB = getDeckMeterLevel(decks.B);
            decks.B.meterDisplayLevel = targetLevelB > decks.B.meterDisplayLevel
                ? targetLevelB
                : Math.max(targetLevelB, decks.B.meterDisplayLevel - decayPerFrame);
            meterBElement.style.height = `${Math.round(decks.B.meterDisplayLevel * 100)}%`;
        }
        meterAnimationFrame = requestAnimationFrame(updateMeterUI);
    }

    function animateCrossfadeTo(targetValue, onComplete = null) {
        stopAutoCrossfade();
        const startValue = parseInt(crossfaderElement.value, 10) || 0;
        autoCrossfadeStartedAt = Date.now();
        const targetDeckName = targetValue < 0 ? "A" : "B";
        const oppositeDeckName = targetDeckName === "A" ? "B" : "A";

        function step() {
            const elapsed = Date.now() - autoCrossfadeStartedAt;
            const progress = clamp(elapsed / autoCrossfadeDurationMs, 0, 1);
            const easedProgress = 1 - Math.pow(1 - progress, 3);
            const nextValue = Math.round(
                startValue + (targetValue - startValue) * easedProgress
            );

            crossfaderElement.value = String(nextValue);
            updateDeckVolumes();

            if (progress >= 1 || nextValue === targetValue) {
                crossfaderElement.value = String(targetValue);
                updateDeckVolumes();
                if (decks[oppositeDeckName]) {
                    decks[oppositeDeckName].audio.pause();
                    decks[oppositeDeckName].audio.currentTime = 0;
                    updateDeckProgress(decks[oppositeDeckName]);
                    updateDeckButtons(decks[oppositeDeckName]);
                }
                if (typeof onComplete === "function") {
                    onComplete();
                }
                autoCrossfadeTimer = null;
                return;
            }
            autoCrossfadeTimer = setTimeout(step, autoCrossfadeStepMs);
        }

        step();
    }

    function updateDeckProgress(deck) {
        const elapsed = Number.isFinite(deck.audio.currentTime) ? deck.audio.currentTime : 0;
        const duration = Number.isFinite(deck.audio.duration) ? deck.audio.duration : 0;
        const percent = duration > 0 ? (elapsed / duration) * 100 : 0;
        deck.progress.style.width = `${clamp(percent, 0, 100)}%`;
        deck.elapsed.textContent = formatSecondsAsTime(elapsed);
        deck.duration.textContent = formatSecondsAsTime(duration);
    }

    function updateDeckButtons(deck) {
        deck.playButton.textContent = deck.audio.paused ? "Play" : "Pause";
        deck.playButton.disabled = !deck.track;
        deck.restartButton.disabled = !deck.track;
        deck.stopButton.disabled = !deck.track;
        updateResultLoadButtons();
    }

    function updateResultLoadButtons() {
        for (const button of Array.from(resultsElement.querySelectorAll("[data-load-deck]"))) {
            const deckName = button.dataset.loadDeck;
            const deck = decks[deckName];
            button.disabled = Boolean(deck && deck.track && !deck.audio.paused);
        }
    }

    function updateDeckDisplay(deck) {
        if (!deck.track) {
            deck.title.textContent = "No track loaded";
            deck.meta.textContent = "Load a search result into this deck.";
            deck.progress.style.width = "0%";
            deck.elapsed.textContent = "00:00";
            deck.duration.textContent = "00:00";
            drawWaveformPlaceholder(deck, "No track loaded");
            updateDeckButtons(deck);
            return;
        }

        deck.title.textContent = deck.track.title || "(untitled)";
        deck.meta.textContent = `${deck.track.artist || "(unknown artist)"} / ${deck.track.album || "(no album)"}`;
        updateDeckProgress(deck);
        updateDeckButtons(deck);
    }

    function getDeckCueInTime(deck) {
        if (!Number.isFinite(deck.cueInTime) || deck.cueInTime <= 0) {
            return 0;
        }
        const duration = Number.isFinite(deck.audio.duration) ? deck.audio.duration : 0;
        return clamp(deck.cueInTime, 0, Math.max(0, duration - 0.25));
    }

    function getDeckFadeTriggerTime(deck) {
        const duration = Number.isFinite(deck.audio.duration) ? deck.audio.duration : 0;
        if (duration <= 0) {
            return 0;
        }
        const defaultTriggerTime = Math.max(0, duration - (autoCrossfadeDurationMs / 1000));
        if (!Number.isFinite(deck.outroStartTime)) {
            return defaultTriggerTime;
        }
        return clamp(Math.min(defaultTriggerTime, deck.outroStartTime), 0, duration);
    }

    function primeDeckCuePoint(deck) {
        const cueInTime = getDeckCueInTime(deck);
        if (cueInTime <= 0 || deck.audio.currentTime > 0.25) {
            return;
        }

        const applyCuePoint = function () {
            if (deck.audio.currentTime > 0.25) {
                return;
            }
            try {
                deck.audio.currentTime = cueInTime;
            } catch (error) {
                /* ignore cue seek failures */
            }
        };

        if (deck.audio.readyState >= 1) {
            applyCuePoint();
        } else {
            deck.audio.addEventListener("loadedmetadata", applyCuePoint, {once: true});
        }
    }

    function unloadDeck(deckName) {
        const deck = decks[deckName];
        deck.track = null;
        deck.normalizationGain = 1;
        deck.outputGain = 0;
        deck.cueInTime = 0;
        deck.outroStartTime = null;
        deck.waveformRequestId += 1;
        deck.audio.pause();
        deck.audio.removeAttribute("src");
        deck.audio.load();
        updateDeckDisplay(deck);
        updateDeckVolumes();
    }

    function loadTrackIntoDeck(deckName, track) {
        const deck = decks[deckName];
        if (!deck.audio.paused) {
            return;
        }
        deck.track = track;
        deck.normalizationGain = 1;
        deck.outputGain = 0;
        deck.cueInTime = 0;
        deck.outroStartTime = null;
        deck.audio.src = track.download_url;
        deck.audio.load();
        updateDeckDisplay(deck);
        updateDeckVolumes();
        if (track.download_url) {
            loadDeckWaveform(deck, track.download_url);
        } else {
            drawWaveformPlaceholder(deck, "Waveform unavailable");
        }
    }

    function ensureDeckPlaying(deckName, useCuePoint = false) {
        const deck = decks[deckName];
        if (!deck.track || !deck.audio.paused) {
            return;
        }
        if (useCuePoint) {
            primeDeckCuePoint(deck);
        }
        deck.audio.play().catch(function () {
            setError(`Unable to start Deck ${deckName} automatically.`);
        });
    }

    function startDeckImmediately(deckName) {
        cancelAutoDjTransition();
        crossfaderElement.value = deckName === "A" ? "-100" : "100";
        updateDeckVolumes();
        ensureDeckPlaying(deckName, true);
    }

    function updateAutoDjToggleUI() {
        if (!autoDjToggleButton) {
            return;
        }
        autoDjToggleButton.textContent = autoDjEnabled ? "Auto DJ On" : "Auto DJ Off";
        autoDjToggleButton.classList.toggle("btn-outline-secondary", !autoDjEnabled);
        autoDjToggleButton.classList.toggle("btn-success", autoDjEnabled);
    }

    function getDominantPlayingDeckName() {
        const playingDecks = Object.values(decks).filter(
            (deck) => deck.track && !deck.audio.paused
        );
        if (playingDecks.length === 0) {
            return null;
        }
        if (playingDecks.length === 1) {
            return playingDecks[0].name;
        }
        return playingDecks.sort(
            (leftDeck, rightDeck) => rightDeck.outputGain - leftDeck.outputGain
        )[0].name;
    }

    async function fetchNextSongForDeck(deckName) {
        const deck = decks[deckName];
        const csrfToken = getCsrfToken();
        if (
            !deck ||
            autoDjFetchInFlight[deckName] ||
            !deck.audio.paused ||
            Date.now() < autoDjRetryAt[deckName]
        ) {
            return;
        }

        autoDjFetchInFlight[deckName] = true;
        try {
            const response = await fetch(app.dataset.nextSongUrl, {
                method: "POST",
                credentials: "same-origin",
                headers: {
                    "X-CSRFToken": csrfToken,
                    "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
                },
                body: new URLSearchParams({csrfmiddlewaretoken: csrfToken}),
            });

            if (!response.ok) {
                throw new Error(`Auto DJ fetch failed with status ${response.status}.`);
            }

            const payload = await response.json();
            if (payload.music_file) {
                autoDjRetryAt[deckName] = 0;
                autoDjQueueTrackId = payload.music_file.id || payload.music_file.pk || null;
                loadTrackIntoDeck(deckName, payload.music_file);
                if (autoDjEnabled) {
                    if (getDominantPlayingDeckName()) {
                        runAutoDj();
                    } else {
                        startDeckImmediately(deckName);
                    }
                }
            } else {
                autoDjRetryAt[deckName] = Date.now() + 5000;
            }
        } catch (error) {
            autoDjRetryAt[deckName] = Date.now() + 5000;
            setError(error instanceof Error ? error.message : "Auto DJ fetch failed.");
        } finally {
            autoDjFetchInFlight[deckName] = false;
        }
    }

    async function syncAutoDjQueueTrack(trackId) {
        const csrfToken = getCsrfToken();
        if (
            !trackId ||
            autoDjSyncInFlight ||
            Date.now() < autoDjSyncRetryAt
        ) {
            return false;
        }

        autoDjSyncInFlight = true;
        try {
            const response = await fetch(app.dataset.nextSongUrl, {
                method: "POST",
                credentials: "same-origin",
                headers: {
                    "X-CSRFToken": csrfToken,
                    "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
                },
                body: new URLSearchParams({
                    csrfmiddlewaretoken: csrfToken,
                    next_track: String(trackId),
                }),
            });

            if (!response.ok) {
                throw new Error(`Auto DJ sync failed with status ${response.status}.`);
            }

            autoDjQueueTrackId = trackId;
            autoDjSyncRetryAt = 0;
            return true;
        } catch (error) {
            autoDjSyncRetryAt = Date.now() + 5000;
            setError(error instanceof Error ? error.message : "Auto DJ sync failed.");
            return false;
        } finally {
            autoDjSyncInFlight = false;
        }
    }

    async function addTrackToQueue(trackId) {
        const csrfToken = getCsrfToken();
        const response = await fetch(app.dataset.addToTagUrl, {
            method: "POST",
            credentials: "same-origin",
            headers: {
                "X-CSRFToken": csrfToken,
                "Content-Type": "application/x-www-form-urlencoded;charset=UTF-8",
            },
            body: new URLSearchParams({
                csrfmiddlewaretoken: csrfToken,
                add_type: "music_file",
                tag: "queue",
                q: String(trackId),
            }),
        });

        if (!response.ok) {
            throw new Error(`Queue add failed with status ${response.status}.`);
        }
    }

    function completeAutoDjTransition(nextActiveDeckName, deckToReload) {
        const activeDeck = decks[nextActiveDeckName];
        const nextActiveTrackId = activeDeck && activeDeck.track
            ? (activeDeck.track.id || activeDeck.track.pk || null)
            : null;

        unloadDeck(deckToReload);
        (async function () {
            if (nextActiveTrackId && autoDjQueueTrackId !== nextActiveTrackId) {
                const synced = await syncAutoDjQueueTrack(nextActiveTrackId);
                if (!synced) {
                    autoDjTransition = null;
                    return;
                }
            }
            autoDjTransition = null;
            fetchNextSongForDeck(deckToReload);
        })();
    }

    function runAutoDj() {
        if (!autoDjEnabled || autoDjTransition) {
            return;
        }

        const activeDeckName = getDominantPlayingDeckName();
        if (!activeDeckName) {
            return;
        }

        const activeDeck = decks[activeDeckName];
        const oppositeDeckName = getOtherDeckName(activeDeckName);
        const oppositeDeck = decks[oppositeDeckName];
        const activeTrackId = activeDeck.track ? (activeDeck.track.id || activeDeck.track.pk || null) : null;

        if (!oppositeDeck.track && !autoDjFetchInFlight[oppositeDeckName]) {
            if (activeTrackId && autoDjQueueTrackId !== activeTrackId) {
                syncAutoDjQueueTrack(activeTrackId);
                return;
            }
            fetchNextSongForDeck(oppositeDeckName);
        }

        if (
            !Number.isFinite(activeDeck.audio.duration) ||
            activeDeck.audio.duration <= 0 ||
            !oppositeDeck.track
        ) {
            return;
        }

        if (activeDeck.audio.currentTime < getDeckFadeTriggerTime(activeDeck)) {
            return;
        }

        autoDjTransition = {from: activeDeckName, to: oppositeDeckName};
        crossfaderElement.value = "0";
        updateDeckVolumes();
        ensureDeckPlaying(oppositeDeckName, true);
        animateCrossfadeTo(oppositeDeckName === "A" ? -100 : 100, function () {
            completeAutoDjTransition(oppositeDeckName, activeDeckName);
        });
    }

    function runManualAutoCrossfade(targetDeckName) {
        const targetDeck = decks[targetDeckName];
        if (!targetDeck || !targetDeck.track) {
            return;
        }

        cancelAutoDjTransition();
        ensureDeckPlaying(targetDeckName, true);

        if (!autoDjEnabled) {
            animateCrossfadeTo(targetDeckName === "A" ? -100 : 100);
            return;
        }

        const deckToReload = getOtherDeckName(targetDeckName);
        autoDjTransition = {from: deckToReload, to: targetDeckName};
        animateCrossfadeTo(targetDeckName === "A" ? -100 : 100, function () {
            completeAutoDjTransition(targetDeckName, deckToReload);
        });
    }

    function seekDeckFromPointerEvent(deck, event) {
        if (!deck.track || !Number.isFinite(deck.audio.duration) || deck.audio.duration <= 0) {
            return;
        }
        const rect = deck.progressTrack.getBoundingClientRect();
        if (rect.width <= 0) {
            return;
        }
        const offset = clamp(event.clientX - rect.left, 0, rect.width);
        const nextPercent = offset / rect.width;
        deck.audio.currentTime = deck.audio.duration * nextPercent;
        updateDeckProgress(deck);
    }

    function bindDeck(deck) {
        deck.playButton.addEventListener("click", function () {
            if (!deck.track) {
                return;
            }
            if (deck.audio.paused) {
                deck.audio.play().catch(function () {
                    /* playback requires user interaction */
                });
            } else {
                deck.audio.pause();
            }
        });

        deck.restartButton.addEventListener("click", function () {
            if (!deck.track) {
                return;
            }
            deck.audio.currentTime = 0;
            deck.audio.play().catch(function () {
                /* playback requires user interaction */
            });
        });

        deck.stopButton.addEventListener("click", function () {
            if (!deck.track) {
                return;
            }
            deck.audio.pause();
            deck.audio.currentTime = 0;
            updateDeckProgress(deck);
            updateDeckButtons(deck);
        });

        deck.audio.addEventListener("timeupdate", function () {
            updateDeckProgress(deck);
            runAutoDj();
        });
        deck.audio.addEventListener("loadedmetadata", function () {
            updateDeckProgress(deck);
            updateDeckButtons(deck);
            runAutoDj();
        });
        deck.audio.addEventListener("play", function () {
            updateDeckButtons(deck);
            runAutoDj();
        });
        deck.audio.addEventListener("pause", function () {
            updateDeckButtons(deck);
        });
        deck.audio.addEventListener("ended", function () {
            deck.audio.currentTime = 0;
            updateDeckProgress(deck);
            updateDeckButtons(deck);
            runAutoDj();
        });

        deck.progressTrack.addEventListener("pointerdown", function (event) {
            if (!deck.track) {
                return;
            }
            deck.isScrubbing = true;
            deck.progressTrack.setPointerCapture(event.pointerId);
            seekDeckFromPointerEvent(deck, event);
        });

        deck.progressTrack.addEventListener("pointermove", function (event) {
            if (!deck.isScrubbing) {
                return;
            }
            seekDeckFromPointerEvent(deck, event);
        });

        deck.progressTrack.addEventListener("pointerup", function (event) {
            if (!deck.isScrubbing) {
                return;
            }
            seekDeckFromPointerEvent(deck, event);
            deck.isScrubbing = false;
            deck.progressTrack.releasePointerCapture(event.pointerId);
        });

        deck.progressTrack.addEventListener("pointercancel", function (event) {
            if (!deck.isScrubbing) {
                return;
            }
            deck.isScrubbing = false;
            deck.progressTrack.releasePointerCapture(event.pointerId);
        });

        updateDeckDisplay(deck);
    }

    function serializeForm(includeOffset) {
        const params = new URLSearchParams();
        const formData = new FormData(form);

        for (const [key, value] of formData.entries()) {
            const trimmed = getTrimmedValue(value);
            if (trimmed === "") {
                continue;
            }
            params.set(key, trimmed);
        }

        if (includeOffset) {
            params.set("offset", String(currentOffset));
        }
        params.set("limit", String(pageSize));
        if (currentOrderBy) {
            params.set("order_by", currentOrderBy);
        }

        return params;
    }

    function syncFormFromUrl(url) {
        const params = new URL(url, window.location.origin).searchParams;
        for (const element of Array.from(form.elements)) {
            if (!element.name) {
                continue;
            }
            element.value = params.get(element.name) || "";
        }
        currentOrderBy = params.get("order_by") || "";
        currentOffset = parseInt(params.get("offset") || "0", 10);
        if (Number.isNaN(currentOffset) || currentOffset < 0) {
            currentOffset = 0;
        }
        updateSortHeaders();
    }

    function pushUrl() {
        const params = serializeForm(true);
        const queryString = params.toString();
        const nextUrl = queryString ? `${window.location.pathname}?${queryString}` : window.location.pathname;
        window.history.pushState({offset: currentOffset}, "", nextUrl);
    }

    function setLoading(loading) {
        spinnerElement.classList.toggle("d-none", !loading);
        app.querySelector("section[aria-live]").setAttribute("aria-busy", loading ? "true" : "false");
    }

    function setError(message) {
        if (message) {
            errorElement.textContent = message;
            errorElement.classList.remove("d-none");
        } else {
            errorElement.textContent = "";
            errorElement.classList.add("d-none");
        }
    }

    function buildBrowseLink(baseUrl, value) {
        const target = new URL(baseUrl, window.location.origin);
        target.searchParams.set("q", value);
        return target.toString();
    }

    function escapeHtml(value) {
        return String(value)
            .replaceAll("&", "&amp;")
            .replaceAll("<", "&lt;")
            .replaceAll(">", "&gt;")
            .replaceAll('"', "&quot;")
            .replaceAll("'", "&#39;");
    }

    function escapeAttribute(value) {
        return escapeHtml(value);
    }

    function renderResult(item) {
        const element = document.createElement("tr");
        const actionMarkup = `
            <div class="d-inline-flex gap-2" role="group" aria-label="Load track into deck">
                <button class="btn btn-outline-${deckColors.A}" type="button" data-load-deck="A">A</button>
                <button class="btn btn-outline-${deckColors.B}" type="button" data-load-deck="B">B</button>
            </div>
        `;

        element.innerHTML = `
            <td>
                <a
                    href="${escapeAttribute(buildBrowseLink(app.dataset.artistUrl, item.artist || ""))}"
                    target="_blank"
                    rel="noopener noreferrer"
                >
                    ${escapeHtml(item.artist || "(unknown artist)")}
                </a>
            </td>
            <td>${escapeHtml(item.title || "(untitled)")}</td>
            <td>
                <a
                    href="${escapeAttribute(buildBrowseLink(app.dataset.albumUrl, item.album || ""))}"
                    target="_blank"
                    rel="noopener noreferrer"
                >
                    ${escapeHtml(item.album || "(no album)")}
                </a>
            </td>
            <td class="text-end music-search-action-cell">${actionMarkup}</td>
        `;

        for (const button of Array.from(element.querySelectorAll("[data-load-deck]"))) {
            button.addEventListener("click", async function () {
                const trackId = item.id || item.pk;
                try {
                    if (!trackId) {
                        throw new Error("Track id is missing from the API result.");
                    }
                    await addTrackToQueue(trackId);
                    loadTrackIntoDeck(button.dataset.loadDeck, item);
                } catch (error) {
                    setError(error instanceof Error ? error.message : "Unable to queue the track.");
                }
            });
        }

        updateResultLoadButtons();

        return element;
    }

    function updateSortHeaders() {
        for (const button of sortButtons) {
            const indicator = button.querySelector(".music-search-sort-indicator");
            const isActive = button.dataset.orderBy === currentOrderBy;
            button.setAttribute("aria-pressed", isActive ? "true" : "false");
            if (indicator) {
                indicator.textContent = isActive ? "↑" : "";
            }
        }
    }

    function updatePagination(limit) {
        const showingStart = totalCount === 0 ? 0 : currentOffset + 1;
        const showingEnd = Math.min(currentOffset + limit, totalCount);
        const currentPage = totalCount === 0 ? 0 : Math.floor(currentOffset / limit) + 1;
        const totalPages = totalCount === 0 ? 0 : Math.ceil(totalCount / limit);

        pageLabelElement.textContent = totalCount === 0
            ? "No results loaded."
            : `Showing ${showingStart}-${showingEnd} of ${totalCount} results. Page ${currentPage} / ${totalPages}.`;

        prevButton.disabled = currentOffset <= 0;
        nextButton.disabled = currentOffset + limit >= totalCount;
    }

    async function runSearch(pushHistory) {
        const params = serializeForm(true);
        const limit = pageSize;
        const query = getTrimmedValue(form.elements.sq.value || "");

        if (!query) {
            totalCount = 0;
            resultsElement.innerHTML = "";
            pageLabelElement.textContent = "";
            prevButton.disabled = true;
            nextButton.disabled = true;
            setError("The full-text search field is required.");
            return;
        }

        setLoading(true);
        setError("");
        requestCounter += 1;
        const requestId = requestCounter;

        try {
            const response = await fetch(`${app.dataset.apiUrl}?${params.toString()}`, {
                headers: {"X-Requested-With": "fetch"},
            });

            if (requestId !== requestCounter) {
                return;
            }

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || `Search failed with status ${response.status}.`);
            }

            const payload = await response.json();
            totalCount = payload.count;
            resultsElement.innerHTML = "";
            for (const item of payload.results) {
                resultsElement.appendChild(renderResult(item));
            }

            updatePagination(limit);
            if (pushHistory) {
                pushUrl();
            }
        } catch (error) {
            if (requestId !== requestCounter) {
                return;
            }
            totalCount = 0;
            resultsElement.innerHTML = "";
            pageLabelElement.textContent = "";
            prevButton.disabled = true;
            nextButton.disabled = true;
            setError(error instanceof Error ? error.message : "Unexpected search error.");
        } finally {
            if (requestId === requestCounter) {
                setLoading(false);
            }
        }
    }

    form.addEventListener("submit", function (event) {
        event.preventDefault();
        currentOffset = 0;
        runSearch(true);
    });

    prevButton.addEventListener("click", function () {
        currentOffset = Math.max(currentOffset - pageSize, 0);
        runSearch(true);
    });

    nextButton.addEventListener("click", function () {
        currentOffset += pageSize;
        runSearch(true);
    });

    for (const button of sortButtons) {
        button.addEventListener("click", function () {
            const nextOrderBy = button.dataset.orderBy || "";
            currentOrderBy = currentOrderBy === nextOrderBy ? "" : nextOrderBy;
            updateSortHeaders();
            currentOffset = 0;
            runSearch(true);
        });
    }

    crossfaderElement.addEventListener("input", function () {
        cancelAutoDjTransition();
        updateDeckVolumes();
    });

    autoCrossfadeAButton.addEventListener("click", function () {
        runManualAutoCrossfade("A");
    });

    autoCrossfadeBButton.addEventListener("click", function () {
        runManualAutoCrossfade("B");
    });

    if (autoDjToggleButton) {
        autoDjToggleButton.addEventListener("click", function () {
            if (autoDjEnabled) {
                autoDjEnabled = false;
                cancelAutoDjTransition();
            } else {
                autoDjEnabled = true;
            }
            updateAutoDjToggleUI();
            runAutoDj();
        });
    }

    window.addEventListener("popstate", function () {
        syncFormFromUrl(window.location.href);
        if (window.location.search) {
            runSearch(false);
        } else {
            form.reset();
            currentOrderBy = "";
            updateSortHeaders();
            currentOffset = 0;
            totalCount = 0;
            resultsElement.innerHTML = "";
            pageLabelElement.textContent = "";
            prevButton.disabled = true;
            nextButton.disabled = true;
            setError("");
        }
    });

    for (const deck of Object.values(decks)) {
        bindDeck(deck);
    }
    window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", function () {
        redrawWaveformsForTheme();
    });
    updateAutoDjToggleUI();
    updateDeckVolumes();
    updateMeterUI();
    updateSortHeaders();
    syncFormFromUrl(window.location.href);
    if (window.location.search) {
        runSearch(false);
    } else {
        prevButton.disabled = true;
        nextButton.disabled = true;
    }
})();
