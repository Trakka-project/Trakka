package io.github.trakka_project.app;

import android.app.Activity;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.pm.PackageInfo;
import androidx.activity.result.ActivityResult;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.ActivityCallback;
import com.getcapacitor.annotation.CapacitorPlugin;
import java.util.Collections;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * The app's own native API, which pages reach as {@code window.Capacitor.Plugins.TrakkaApp}: the
 * connect screen (android/www) drives it, Trakka's settings (static/js/settings.js) use
 * {@code getServer} and {@code changeServer}, and its sign-in page (static/js/login.js)
 * {@code signInWithBrowser}. Whatever can point the app to another server is
 * refused while a server's pages are shown, so no page can send the app elsewhere: only the user
 * can, from the connect screen.
 */
@CapacitorPlugin(name = "TrakkaApp")
public class TrakkaAppPlugin extends Plugin {

    // Keeps the status and navigation bar icons readable against what is behind them. When
    // Capacitor's SystemBars passes the insets through to the page (its rule: WebView 140+ and a
    // viewport-fit=cover page, as Trakka's are), the page draws under the bars, so the icons follow
    // the page's theme: Trakka sets data-theme="light"|"dark" on <html> (static/js/theme-init.js)
    // and may be set to a theme the system is not in. Otherwise the bars sit on the window's own
    // background, which follows the system, and so do the icons ("DEFAULT").
    private static final String THEME_SYNC_SCRIPT =
        """
        (function () {
          var passthroughWebView = __WEBVIEW_MAJOR__ >= 140;
          var applied = null;
          function style() {
            var viewport = document.querySelector('meta[name="viewport"]');
            var cover = !!viewport && /viewport-fit=cover/.test(viewport.getAttribute('content') || '');
            var theme = document.documentElement && document.documentElement.getAttribute('data-theme');
            if (!passthroughWebView || !cover || (theme !== 'dark' && theme !== 'light')) return 'DEFAULT';
            return theme === 'dark' ? 'DARK' : 'LIGHT';
          }
          function sync() {
            var bars = window.Capacitor && window.Capacitor.Plugins && window.Capacitor.Plugins.SystemBars;
            var next = style();
            if (!bars || next === applied) return;
            applied = next;
            bars.setStyle({ style: next });
          }
          if (document.documentElement) {
            new MutationObserver(sync).observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
          }
          document.addEventListener('DOMContentLoaded', sync);
          sync();
        })();
        """;

    private final ExecutorService network = Executors.newSingleThreadExecutor();
    private ServerStore store;

    @Override
    public void load() {
        store = new ServerStore(getContext());
        String server = getBridge().getServerUrl();
        // Plugins load before Capacitor starts loading the server's first page.
        if (server != null && WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)) {
            String script = THEME_SYNC_SCRIPT.replace("__WEBVIEW_MAJOR__", String.valueOf(webViewMajorVersion()));
            WebViewCompat.addDocumentStartJavaScript(getBridge().getWebView(), script, Collections.singleton(server));
        }
    }

    private int webViewMajorVersion() {
        PackageInfo webView = WebViewCompat.getCurrentWebViewPackage(getContext());
        try {
            return webView == null || webView.versionName == null ? 0 : Integer.parseInt(webView.versionName.split("\\.")[0]);
        } catch (NumberFormatException e) {
            return 0;
        }
    }

    @Override
    protected void handleOnDestroy() {
        network.shutdownNow();
    }

    /** The server the app opens, and the app's version: for Trakka's settings. */
    @PluginMethod
    public void getServer(PluginCall call) {
        JSObject result = new JSObject();
        result.put("server", store.current());
        result.put("appVersion", activity().appVersion());
        call.resolve(result);
    }

    /**
     * Runs the server's SSO sign-in in the phone's browser, where an identity provider can ask for
     * a passkey or a security key (see {@link BrowserSignIn}); MainActivity takes the session back.
     * For Trakka's sign-in page (static/js/login.js), which falls back to the WebView on rejection.
     */
    @PluginMethod
    public void signInWithBrowser(PluginCall call) {
        String server = activity().server();
        if (server == null) {
            call.unavailable("Only available on a server's pages");
            return;
        }
        try {
            getActivity().startActivity(BrowserSignIn.browserIntent(new BrowserSignIn(getContext()).start(server)));
            call.resolve();
        } catch (ActivityNotFoundException e) {
            call.reject("No browser to sign in with", "no_browser");
        }
    }

    /** Opens the connect screen, from which "Annuler" comes back to the current server. */
    @PluginMethod
    public void changeServer(PluginCall call) {
        store.setChangeRequested(true);
        call.resolve();
        activity().restart();
    }

    /** What the connect screen shows: {server, recent, canCancel, error?}. */
    @PluginMethod
    public void getConnectState(PluginCall call) {
        if (!onConnectScreen(call)) {
            return;
        }
        JSObject state = new JSObject();
        state.put("server", store.current());
        state.put("recent", new JSArray(store.recent()));
        state.put("canCancel", activity().canCancelChange());
        MainActivity.LoadError error = MainActivity.pendingError;
        if (error != null) {
            JSObject details = new JSObject();
            details.put("server", error.server);
            details.put("host", error.host);
            details.put("reason", error.reason);
            details.put("status", error.status);
            state.put("error", details);
        }
        call.resolve(state);
    }

    /** {@link ServerProbe#check} for an address as typed or scanned. */
    @PluginMethod
    public void checkServer(PluginCall call) {
        if (!onConnectScreen(call)) {
            return;
        }
        String origin = normalizedUrl(call);
        if (origin != null) {
            network.execute(() -> call.resolve(ServerProbe.check(origin)));
        }
    }

    /** Makes the app open this server from now on. */
    @PluginMethod
    public void connect(PluginCall call) {
        if (!onConnectScreen(call)) {
            return;
        }
        String origin = normalizedUrl(call);
        if (origin != null) {
            MainActivity.pendingError = null;
            store.connect(origin);
            call.resolve();
            activity().restart();
        }
    }

    @PluginMethod
    public void forgetServer(PluginCall call) {
        if (!onConnectScreen(call)) {
            return;
        }
        String origin = normalizedUrl(call);
        if (origin != null) {
            store.forget(origin);
            call.resolve();
        }
    }

    /** Back to the current server: "Annuler" after "Changer de serveur", "Réessayer" after an error. */
    @PluginMethod
    public void reopenServer(PluginCall call) {
        if (!onConnectScreen(call)) {
            return;
        }
        if (store.current() == null) {
            call.reject("No server to go back to", "no_server");
            return;
        }
        MainActivity.pendingError = null;
        store.setChangeRequested(false);
        call.resolve();
        activity().restart();
    }

    /** Resolves {text} with the scanned code's content; rejects "cancelled" or "camera_denied". */
    @PluginMethod
    public void scanQrCode(PluginCall call) {
        if (onConnectScreen(call)) {
            startActivityForResult(call, new Intent(getContext(), QrScanActivity.class), "onQrScanned");
        }
    }

    @ActivityCallback
    private void onQrScanned(PluginCall call, ActivityResult result) {
        if (call == null) {
            return;
        }
        Intent data = result.getData();
        if (result.getResultCode() == Activity.RESULT_OK && data != null && data.getStringExtra(QrScanActivity.EXTRA_TEXT) != null) {
            JSObject scanned = new JSObject();
            scanned.put("text", data.getStringExtra(QrScanActivity.EXTRA_TEXT));
            call.resolve(scanned);
        } else if (result.getResultCode() == QrScanActivity.RESULT_CAMERA_DENIED) {
            call.reject("Camera permission denied", "camera_denied");
        } else {
            call.reject("Scan cancelled", "cancelled");
        }
    }

    private MainActivity activity() {
        return (MainActivity) getActivity();
    }

    private boolean onConnectScreen(PluginCall call) {
        if (activity().isConnectScreen()) {
            return true;
        }
        call.unavailable("Only available on the app's connect screen");
        return false;
    }

    /** The call's "url" as a server origin, or null after rejecting the call with the reason. */
    private static String normalizedUrl(PluginCall call) {
        try {
            return ServerStore.normalize(call.getString("url"));
        } catch (ServerStore.InvalidServerException e) {
            call.reject("Invalid server address", e.reason);
            return null;
        }
    }
}
