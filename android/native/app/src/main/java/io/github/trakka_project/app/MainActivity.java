package io.github.trakka_project.app;

import android.content.Intent;
import android.graphics.Bitmap;
import android.net.Uri;
import android.net.http.SslError;
import android.os.Bundle;
import android.view.ViewGroup;
import android.webkit.GeolocationPermissions;
import android.webkit.PermissionRequest;
import android.webkit.RenderProcessGoneDetail;
import android.webkit.SslErrorHandler;
import android.webkit.WebBackForwardList;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import androidx.activity.OnBackPressedCallback;
import androidx.annotation.Nullable;
import androidx.core.content.ContextCompat;
import androidx.core.splashscreen.SplashScreen;
import androidx.webkit.WebViewFeature;
import com.getcapacitor.Bridge;
import com.getcapacitor.BridgeActivity;
import com.getcapacitor.BridgeWebChromeClient;
import com.getcapacitor.BridgeWebViewClient;
import com.getcapacitor.CapConfig;
import com.getcapacitor.WebViewListener;
import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.regex.Pattern;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * The app's only activity: Capacitor's WebView, showing either the connect screen bundled in the
 * app (android/www, served at https://localhost) or the Trakka server chosen there, loaded straight
 * from that server as a browser would, with its own headers. Which of the two is decided when the
 * activity is created, so switching between them recreates it ({@link #restart()}).
 *
 * <p>Signing in happens on the connect screen too: when the server sends the WebView to its
 * sign-in page, the activity shows the app's own sign-in screen instead ({@link #pendingSignIn}),
 * which offers the server's methods (SSO through the browser, its password page) and a way to
 * change server.
 */
public class MainActivity extends BridgeActivity {

    /**
     * Why the configured server could not be opened, explained on the connect screen. Kept in
     * memory only: the next launch tries the server again.
     */
    @Nullable
    static volatile LoadError pendingError;

    /**
     * The server whose sign-in page was asked for, which the connect screen shows as the app's own
     * sign-in screen. In memory only, like {@link #pendingError}: the next launch opens the server,
     * which asks again.
     */
    @Nullable
    static volatile SignInRequest pendingSignIn;

    /**
     * Set by the sign-in screen's "password" or "create an account" choice (or its SSO without a
     * browser): the server's own page to open, which the next server activity then shows instead
     * of going back to the sign-in screen.
     */
    @Nullable
    static volatile String pendingServerSignInPath;

    /** The end of an SSO sign-in that came back to the connect screen, for the server to finish. */
    @Nullable
    static volatile Uri pendingSignInReturn;

    /**
     * A sign-in QR code scanned on the connect screen (TrakkaAppPlugin#connect), for its server to
     * redeem once opened. In memory only, like {@link #pendingSignIn}: the code expires in minutes.
     */
    @Nullable
    static volatile QrSignIn pendingQrSignIn;

    /** Where the server redeems a sign-in QR code (internal/handlers/qr_login.go). */
    private static final String QR_SIGN_IN_PATH = "/auth/qr-code/login";

    private static final Pattern ERROR_CODE = Pattern.compile("[a-z_]{1,64}");

    // Past this, a slow server shows its own loading state rather than the splash screen.
    private static final long SPLASH_MAX_MS = 2500;

    private ServerStore store;
    private BrowserSignIn browserSignIn;
    /** Origin of the Trakka server this activity shows; null on the connect screen. */
    @Nullable
    private String server;
    private volatile boolean firstPagePainted;
    private boolean restarting;
    /** True while the server's own sign-in pages are shown, as the user chose on the sign-in screen. */
    private boolean serverSignInAllowed;
    private boolean sawServerSignIn;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        SplashScreen.installSplashScreen(this).setKeepOnScreenCondition(() -> !firstPagePainted);
        store = new ServerStore(this);
        browserSignIn = new BrowserSignIn(this);
        ReminderChannels.create(this);
        SignInRequest signIn = pendingSignIn;
        if (signIn != null && !signIn.server.equals(store.current())) {
            pendingSignIn = signIn = null;
        }
        server = store.changeRequested() || pendingError != null || signIn != null ? null : store.current();
        if (server != null && !WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)) {
            // Without it, Capacitor would proxy the server's pages to insert its bridge, and drop
            // their response headers on the way (Trakka's Content-Security-Policy among them).
            pendingError = new LoadError(server, Uri.parse(server).getAuthority(), "webview_outdated", 0);
            server = null;
        }
        if (server != null) {
            config = serverConfig(server);
        }
        registerPlugin(TrakkaAppPlugin.class);
        super.onCreate(savedInstanceState);
        if (bridge == null) {
            // No usable WebView: Capacitor shows its own explanation instead.
            firstPagePainted = true;
            return;
        }

        WebView webView = bridge.getWebView();
        webView.setBackgroundColor(ContextCompat.getColor(this, R.color.app_background));
        bridge.setWebViewClient(new TrakkaWebViewClient(bridge));
        webView.setWebChromeClient(new TrakkaWebChromeClient(bridge));
        bridge.addWebViewListener(
            new WebViewListener() {
                @Override
                public void onPageCommitVisible(WebView view, String url) {
                    firstPagePainted = true;
                }
            }
        );
        webView.postDelayed(() -> firstPagePainted = true, SPLASH_MAX_MS);
        getOnBackPressedDispatcher().addCallback(this, new BackCallback());
        if (server != null) {
            String signInPath = pendingServerSignInPath;
            pendingServerSignInPath = null;
            if (signInPath != null) {
                serverSignInAllowed = true;
                webView.loadUrl(server + signInPath);
            }
            Uri signInReturn = pendingSignInReturn;
            pendingSignInReturn = null;
            if (signInReturn != null) {
                finishSignIn(signInReturn);
            }
            QrSignIn qrSignIn = pendingQrSignIn;
            pendingQrSignIn = null;
            if (qrSignIn != null && qrSignIn.server.equals(server)) {
                redeemQrSignIn(qrSignIn);
            }
        }
        // Android may have stopped the app while an SSO sign-in went on in the browser.
        handleSignInReturn(getIntent());
    }

    @Override
    public void onDestroy() {
        WebView webView = bridge == null ? null : bridge.getWebView();
        super.onDestroy();
        // Capacitor leaves its WebView to the garbage collector: until then, the page it showed
        // (another server's Trakka, after a switch) would stay alive in the background.
        if (webView != null) {
            if (webView.getParent() instanceof ViewGroup) {
                ((ViewGroup) webView.getParent()).removeView(webView);
            }
            webView.destroy();
        }
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        handleSignInReturn(intent);
        // The launcher shortcut (ChangeServerActivity) asked for the connect screen.
        if (server != null && store.changeRequested()) {
            restart();
        } else if (server == null && pendingSignIn != null && store.changeRequested()) {
            pendingSignIn = null;
            restart();
        }
    }

    boolean isConnectScreen() {
        return server == null;
    }

    /** Origin of the Trakka server shown; null on the connect screen. */
    @Nullable
    String server() {
        return server;
    }

    /** True when the connect screen was opened from a working server, which it can go back to. */
    boolean canCancelChange() {
        return server == null && pendingError == null && store.changeRequested() && store.current() != null;
    }

    String appVersion() {
        return BuildConfig.VERSION_NAME;
    }

    /** Shows the connect screen or the server, whichever the stored state now calls for. */
    void restart() {
        runOnUiThread(() -> {
            if (!restarting && !isFinishing()) {
                restarting = true;
                recreate();
            }
        });
    }

    /**
     * The end of an SSO sign-in run in the browser (TrakkaAppPlugin#signInWithBrowser), which
     * Android brings back here: the WebView then opens the session it hands over. A sign-in
     * started from the sign-in screen comes back to it, which then opens the server to finish.
     */
    private void handleSignInReturn(@Nullable Intent intent) {
        Uri url = intent == null ? null : intent.getData();
        if (!BrowserSignIn.isReturn(url)) {
            return;
        }
        // Once only: a recreated activity is given the same intent again.
        setIntent(new Intent(intent).setData(null));
        if (bridge == null) {
            return;
        }
        if (server == null) {
            String current = store.current();
            if (current != null && current.equals(browserSignIn.pendingServer())) {
                pendingSignInReturn = url;
                pendingSignIn = null;
                pendingError = null;
                store.setChangeRequested(false);
                restart();
            }
            return;
        }
        finishSignIn(url);
    }

    private void finishSignIn(Uri url) {
        BrowserSignIn.Next next = browserSignIn.finish(url, server);
        if (next == null) {
            return;
        }
        WebView webView = bridge.getWebView();
        if (next.postData != null) {
            webView.postUrl(next.url, next.postData);
        } else {
            webView.loadUrl(next.url);
        }
    }

    /**
     * Opens a session with a sign-in QR code's code: a form POST from the WebView, as for an SSO
     * sign-in, so the session cookie lands where Trakka's pages will use it. The server answers
     * with a redirect to Trakka, or a 401 for a code expired or already used, which
     * {@link TrakkaWebViewClient#onReceivedHttpError} turns into the sign-in screen.
     */
    private void redeemQrSignIn(QrSignIn qrSignIn) {
        byte[] form = ("token=" + Uri.encode(qrSignIn.code)).getBytes(StandardCharsets.UTF_8);
        bridge.getWebView().postUrl(server + QR_SIGN_IN_PATH, form);
    }

    /**
     * Shows the app's sign-in screen instead of the server's sign-in page, unless the user chose
     * that page there. True when it does, the page being left behind.
     */
    private boolean showSignInScreen(Uri url) {
        if (server == null || restarting || !sameOrigin(url, server)) {
            return false;
        }
        String path = String.valueOf(url.getPath());
        if (!path.startsWith("/auth/")) {
            // Past the server's sign-in pages, signed in: its next sign-in page is for the app's
            // screen again (after signing out, for one).
            if (sawServerSignIn) {
                serverSignInAllowed = false;
            }
            return false;
        }
        sawServerSignIn = true;
        if (serverSignInAllowed || !path.equals("/auth/login") || url.getQueryParameter("mode") != null) {
            return false;
        }
        String error = url.getQueryParameter("error");
        pendingSignIn = new SignInRequest(server, error != null && ERROR_CODE.matcher(error).matches() ? error : null);
        restart();
        return true;
    }

    private void showLoadError(Uri url, String reason, int status) {
        if (server != null && !restarting) {
            pendingError = new LoadError(server, url.getAuthority(), reason, status);
            restart();
        }
    }

    /**
     * capacitor.config.json with server.url set to the chosen server: Capacitor then loads it
     * directly, and gives its pages (and only them, see TrakkaAppPlugin) the native bridge.
     * Written to the app's private files so that Capacitor loads it like its own, debug build
     * detection included.
     */
    private CapConfig serverConfig(String origin) {
        File dir = new File(getNoBackupFilesDir(), "server-config");
        try (InputStream in = getAssets().open("capacitor.config.json")) {
            JSONObject json = new JSONObject(Streams.readUtf8(in, 256 * 1024));
            JSONObject serverJson = json.optJSONObject("server");
            if (serverJson == null) {
                serverJson = new JSONObject();
                json.put("server", serverJson);
            }
            serverJson.put("url", origin);
            if (!dir.isDirectory() && !dir.mkdirs()) {
                throw new IOException("Cannot create " + dir);
            }
            try (OutputStream out = new FileOutputStream(new File(dir, "capacitor.config.json"))) {
                out.write(json.toString().getBytes(StandardCharsets.UTF_8));
            }
        } catch (IOException | JSONException e) {
            throw new IllegalStateException("Cannot write the server's Capacitor configuration", e);
        }
        CapConfig loaded = CapConfig.loadFromFile(this, dir.getAbsolutePath());
        if (!origin.equals(loaded.getServerUrl())) {
            throw new IllegalStateException("Capacitor did not load the server's configuration");
        }
        return loaded;
    }

    private static boolean isWeb(Uri url) {
        return "https".equals(url.getScheme()) || "http".equals(url.getScheme());
    }

    private static boolean sameOrigin(Uri url, String origin) {
        Uri expected = Uri.parse(origin);
        return (
            "https".equals(url.getScheme()) &&
            expected.getHost() != null &&
            expected.getHost().equalsIgnoreCase(url.getHost()) &&
            portOf(expected) == portOf(url)
        );
    }

    private static int portOf(Uri url) {
        return url.getPort() == -1 ? 443 : url.getPort();
    }

    private static String reasonFor(int errorCode) {
        switch (errorCode) {
            case WebViewClient.ERROR_HOST_LOOKUP:
                return "unknown_host";
            case WebViewClient.ERROR_TIMEOUT:
                return "timeout";
            case WebViewClient.ERROR_FAILED_SSL_HANDSHAKE:
                return "tls";
            case WebViewClient.ERROR_CONNECT:
                return "unreachable";
            default:
                return "network";
        }
    }

    static final class LoadError {

        final String server;
        final String host;
        final String reason;
        final int status;

        LoadError(String server, String host, String reason, int status) {
            this.server = server;
            this.host = host;
            this.reason = reason;
            this.status = status;
        }
    }

    /** A sign-in QR code's code, and the server it is for. */
    static final class QrSignIn {

        final String server;
        final String code;

        QrSignIn(String server, String code) {
            this.server = server;
            this.code = code;
        }
    }

    /** The server whose sign-in page was asked for, and the error it came with, if any. */
    static final class SignInRequest {

        final String server;

        @Nullable
        final String error;

        SignInRequest(String server, @Nullable String error) {
            this.server = server;
            this.error = error;
        }
    }

    private final class TrakkaWebViewClient extends BridgeWebViewClient {

        TrakkaWebViewClient(Bridge bridge) {
            super(bridge);
        }

        @Override
        public void onPageStarted(WebView view, String url, Bitmap favicon) {
            super.onPageStarted(view, url, favicon);
            // Covers what shouldOverrideUrlLoading doesn't see: pages loaded by the app itself, and
            // redirects answering a form POST (signing out).
            if (url != null && showSignInScreen(Uri.parse(url))) {
                view.stopLoading();
            }
        }

        @Override
        public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
            Uri url = request.getUrl();
            if (request.isForMainFrame() && showSignInScreen(url)) {
                return true;
            }
            if (
                server != null &&
                request.isForMainFrame() &&
                isWeb(url) &&
                !sameOrigin(url, server) &&
                (request.isRedirect() || !sameOrigin(Uri.parse(String.valueOf(view.getUrl())), server))
            ) {
                // An SSO sign-in bounces through its identity provider, an authentication proxy in
                // front of Trakka through its portal: redirects, and the pages they lead to, stay in
                // the app. A link the user follows from Trakka to another site opens in the
                // browser (Capacitor's default, below).
                return false;
            }
            return super.shouldOverrideUrlLoading(view, request);
        }

        @Override
        public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
            super.onReceivedError(view, request, error);
            // ERR_ABORTED is a navigation replaced by another one, not a failure.
            if (request.isForMainFrame() && !String.valueOf(error.getDescription()).contains("ERR_ABORTED")) {
                showLoadError(request.getUrl(), reasonFor(error.getErrorCode()), 0);
            }
        }

        @Override
        public void onReceivedHttpError(WebView view, WebResourceRequest request, WebResourceResponse response) {
            super.onReceivedHttpError(view, request, response);
            int status = response.getStatusCode();
            // A sign-in QR code refused (expired, already used, or too many attempts): back to the
            // sign-in screen, which says why.
            if (
                server != null &&
                request.isForMainFrame() &&
                sameOrigin(request.getUrl(), server) &&
                QR_SIGN_IN_PATH.equals(request.getUrl().getPath()) &&
                status < 500 &&
                !restarting
            ) {
                pendingSignIn = new SignInRequest(server, status == 429 ? "rate_limited" : "qr_login_failed");
                restart();
                return;
            }
            // A 5xx for Trakka's own page means it is down behind its proxy. Any other status is a
            // page to show (a 404 for a stale link, an authentication portal's 401...).
            if (server != null && request.isForMainFrame() && status >= 500 && sameOrigin(request.getUrl(), server)) {
                showLoadError(request.getUrl(), "http", response.getStatusCode());
            }
        }

        @Override
        public void onReceivedSslError(WebView view, SslErrorHandler handler, SslError error) {
            handler.cancel();
            Uri url = Uri.parse(error.getUrl());
            if (server != null && sameOrigin(url, server)) {
                showLoadError(url, "tls", 0);
            }
        }

        @Override
        public boolean onRenderProcessGone(WebView view, RenderProcessGoneDetail detail) {
            super.onRenderProcessGone(view, detail);
            // The page's renderer died (often killed for memory while in the background): start
            // over rather than letting Android kill the whole app.
            restart();
            return true;
        }
    }

    private static final class TrakkaWebChromeClient extends BridgeWebChromeClient {

        TrakkaWebChromeClient(Bridge bridge) {
            super(bridge);
        }

        // Trakka's pages get neither the camera, the microphone nor the location. Capacitor would
        // grant them to any page, with no prompt of its own, as soon as the app holds the Android
        // permission (it has CAMERA, for QrScanActivity): refuse them all. The barcode scanner of
        // "Courses" lists goes through the native scanner instead (TrakkaAppPlugin#scanBarcode),
        // which hands the page a code's digits, never the camera.
        @Override
        public void onPermissionRequest(PermissionRequest request) {
            request.deny();
        }

        @Override
        public void onGeolocationPermissionsShowPrompt(String origin, GeolocationPermissions.Callback callback) {
            callback.invoke(origin, false, false);
        }
    }

    private final class BackCallback extends OnBackPressedCallback {

        BackCallback() {
            super(true);
        }

        @Override
        public void handleOnBackPressed() {
            WebView webView = bridge.getWebView();
            if (serverSignInAllowed && onServerSignInPage(webView)) {
                // Back from the server's sign-in page chosen on the sign-in screen: to that screen.
                pendingSignIn = new SignInRequest(server, null);
                restart();
            } else if (canGoBackInApp(webView)) {
                webView.goBack();
            } else if (canCancelChange()) {
                store.setChangeRequested(false);
                restart();
            } else {
                // Leave the app, as Android does by default.
                setEnabled(false);
                getOnBackPressedDispatcher().onBackPressed();
                setEnabled(true);
            }
        }

        private boolean onServerSignInPage(WebView webView) {
            Uri current = Uri.parse(String.valueOf(webView.getUrl()));
            return server != null && sameOrigin(current, server) && String.valueOf(current.getPath()).startsWith("/auth/");
        }

        // Going back to a sign-in page, or to an identity provider's after an SSO sign-in, would
        // only bounce forward again: from the first page past them, back leaves the app instead.
        private boolean canGoBackInApp(WebView webView) {
            WebBackForwardList history = webView.copyBackForwardList();
            int current = history.getCurrentIndex();
            if (current < 1) {
                return false;
            }
            if (server == null) {
                return true;
            }
            Uri previous = Uri.parse(history.getItemAtIndex(current - 1).getUrl());
            return sameOrigin(previous, server) && !String.valueOf(previous.getPath()).startsWith("/auth/");
        }
    }
}
