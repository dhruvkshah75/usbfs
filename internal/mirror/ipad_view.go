package mirror

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Keep the upstream viewer and its input handlers intact, and change only its presentation.
const screenOnlyView = `<style>
html[data-usbfs-screen] {
	--usbfs-border: 3px;
	--usbfs-video-width: calc(100vw - 2 * var(--usbfs-border));
	--usbfs-video-height: calc(100dvh - 2 * var(--usbfs-border));
}
html[data-usbfs-screen] body {
	margin: 0;
	padding: 0;
	background: #000;
	overflow: hidden;
}
html[data-usbfs-screen] :is(#topbar, #left-tray, #right-tray, #bottom-row, #device-frame .hw),
html[data-usbfs-screen] #stage::before {
	display: none;
}
html[data-usbfs-screen] #workspace {
	align-items: center;
	height: 100dvh;
}
html[data-usbfs-screen] #stage-wrap {
	padding: 0;
	max-height: 100dvh;
	overflow: visible;
}
html[data-usbfs-screen] #device-frame {
	padding: 0;
	border: var(--usbfs-border) solid #625d58;
	border-radius: 0;
	box-shadow: none;
	transform: none !important;
	transition: none !important;
}
html[data-usbfs-screen] canvas#c {
	width: min(var(--usbfs-video-width), calc(var(--usbfs-video-height) * var(--usbfs-ratio, 1))) !important;
	height: min(var(--usbfs-video-height), calc(var(--usbfs-video-width) / var(--usbfs-ratio, 1))) !important;
	border-radius: 0;
}
html[data-usbfs-screen] #offline-overlay {
	inset: 0;
	border-radius: 0;
}
</style>
<script>
const usbfsRoot = document.documentElement;
usbfsRoot.setAttribute('data-usbfs-screen', '');
window.addEventListener('DOMContentLoaded', () => {
	const canvas = document.getElementById('c');
	if (!canvas) return;
	let fittedRatio;
	const fit = () => {
		// Wait for the decoded frame rather than using the canvas defaults.
		if (!canvas.hasAttribute('width') || !canvas.hasAttribute('height')) return;
		const {width, height} = canvas;
		if (!width || !height) return;
		const ratio = width / height;
		canvas.style.setProperty('--usbfs-ratio', ratio);
		if (location.hash !== '#usbfs-app' || !usbfsRoot.hasAttribute('data-usbfs-screen') ||
			ratio === fittedRatio) return;
		fittedRatio = ratio;
		const border = 2 * parseFloat(getComputedStyle(usbfsRoot).getPropertyValue('--usbfs-border'));
		const frameWidth = outerWidth - innerWidth + border;
		const frameHeight = outerHeight - innerHeight + border;
		const scale = Math.min(
			(Math.max(innerWidth, innerHeight) - border) / Math.max(width, height),
			(screen.availWidth - frameWidth) / width,
			(screen.availHeight - frameHeight) / height,
		);
		window.resizeTo(Math.round(width * scale) + frameWidth, Math.round(height * scale) + frameHeight);
	};
	fit();
	const observer = new MutationObserver(fit);
	observer.observe(canvas, {attributeFilter: ['width', 'height']});
	observer.observe(usbfsRoot, {attributeFilter: ['data-usbfs-screen']});
});
for (const type of ['keydown', 'keyup']) {
	window.addEventListener(type, (event) => {
		if (event.key !== 'F2' || event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
		event.preventDefault();
		event.stopImmediatePropagation();
		if (type === 'keydown' && !event.repeat) {
			usbfsRoot.toggleAttribute('data-usbfs-screen');
			window.dispatchEvent(new Event('resize'));
		}
	}, true);
}
</script>
`

func startScreenOnlyViewer(upstream string) (string, func(), error) {
	target, err := url.Parse(upstream)
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	host := listener.Addr().String()
	logger := log.New(io.Discard, "", 0)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(req *httputil.ProxyRequest) {
			req.SetURL(target)
			// pymobiledevice3 validates the browser's Origin against Host.
			req.Out.Host = req.In.Host
		},
		FlushInterval:  -1,
		ModifyResponse: addScreenOnlyView,
		ErrorLog:       logger,
	}
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          logger,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != host {
				http.Error(w, "Invalid viewer host", http.StatusBadRequest)
				return
			}
			proxy.ServeHTTP(w, r)
		}),
	}
	go func() { _ = server.Serve(listener) }()
	return "http://" + host + "/", func() {
		_ = server.Close()
		_ = listener.Close()
	}, nil
}

func addScreenOnlyView(resp *http.Response) error {
	path := resp.Request.URL.Path
	if resp.Request.Method != http.MethodGet || resp.StatusCode != http.StatusOK ||
		(path != "/" && path != "/index.html") {
		return nil
	}
	const maxViewerHTML = 256 << 10
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxViewerHTML+1))
	if err != nil {
		return err
	}
	if len(body) > maxViewerHTML {
		return fmt.Errorf("iPad viewer page exceeds %d bytes", maxViewerHTML)
	}
	body = bytes.Replace(body, []byte("</head>"), []byte(screenOnlyView+"</head>"), 1)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Length")
	resp.Header.Del("ETag")
	return nil
}
