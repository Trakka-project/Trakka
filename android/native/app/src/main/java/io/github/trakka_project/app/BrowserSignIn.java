package io.github.trakka_project.app;

import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.Uri;
import android.os.Bundle;
import android.util.Base64;
import androidx.annotation.Nullable;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.security.SecureRandom;
import java.util.regex.Pattern;

/**
 * An SSO sign-in run in the phone's browser instead of the app's WebView. The WebView cannot use
 * WebAuthn on an identity provider's site (Android only allows it for sites tied to the app by
 * Digital Asset Links), so a provider asking for a passkey or a security key fails there; the
 * browser has no such limit, and may already be signed in to the provider.
 *
 * <p>The server runs the whole flow in the browser and ends it on {@link #RETURN_SCHEME}://sso
 * with a single-use code, which only opens a session together with the verifier kept here (see
 * internal/handlers/oidc_app.go): another app declaring the same scheme gets nothing usable.
 */
final class BrowserSignIn {

    /** The deep link the server ends the sign-in on, declared in AndroidManifest.xml. */
    static final String RETURN_SCHEME = "io.github.trakka-project.app";
    static final String RETURN_HOST = "sso";

    // The server's own limit for completing a sign-in (its OIDC flow cookie).
    private static final long MAX_AGE_MS = 10 * 60 * 1000;
    private static final Pattern CODE = Pattern.compile("[A-Za-z0-9_-]{1,128}");
    private static final Pattern ERROR = Pattern.compile("[a-z_]{1,64}");

    // Kept in the app's private storage rather than in memory: Android may stop the app while the
    // user is in the browser.
    private static final String PREFS = "trakka_browser_sign_in";
    private static final String KEY_SERVER = "server";
    private static final String KEY_VERIFIER = "verifier";
    private static final String KEY_STARTED_AT = "started_at";

    private final SharedPreferences prefs;

    BrowserSignIn(Context context) {
        prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    /** Starts a sign-in on server, replacing any unfinished one: the URL to open in the browser. */
    Uri start(String server) {
        byte[] random = new byte[32];
        new SecureRandom().nextBytes(random);
        String verifier = base64Url(random);
        prefs
            .edit()
            .putString(KEY_SERVER, server)
            .putString(KEY_VERIFIER, verifier)
            .putLong(KEY_STARTED_AT, System.currentTimeMillis())
            .commit();
        return Uri.parse(server + "/auth/oidc/login").buildUpon().appendQueryParameter("app_challenge", challenge(verifier)).build();
    }

    /** Whether url is the end of a sign-in, whichever app it was meant for. */
    static boolean isReturn(@Nullable Uri url) {
        return url != null && RETURN_SCHEME.equals(url.getScheme()) && RETURN_HOST.equals(url.getHost());
    }

    /**
     * What the WebView showing server must do with the end of a sign-in: null when it is not one
     * this app is waiting for on that server. Either way, the unfinished sign-in is over.
     */
    @Nullable
    Next finish(Uri url, String server) {
        String startedOn = prefs.getString(KEY_SERVER, null);
        String verifier = prefs.getString(KEY_VERIFIER, null);
        long startedAt = prefs.getLong(KEY_STARTED_AT, 0);
        prefs.edit().clear().commit();
        long age = System.currentTimeMillis() - startedAt;
        if (!isReturn(url) || verifier == null || !server.equals(startedOn) || age < 0 || age > MAX_AGE_MS) {
            return null;
        }

        String code = url.getQueryParameter("code");
        if (code != null && CODE.matcher(code).matches()) {
            String form = "code=" + Uri.encode(code) + "&verifier=" + Uri.encode(verifier);
            return new Next(server + "/auth/oidc/app-session", form.getBytes(StandardCharsets.UTF_8));
        }
        // The server's own error codes, shown by its sign-in page.
        String error = url.getQueryParameter("error");
        if (error == null || !ERROR.matcher(error).matches()) {
            error = "oidc_failed";
        }
        return new Next(server + "/auth/login?error=" + error, null);
    }

    /**
     * Opens url in a Custom Tab: the browser's own tab, over the app. Custom Tabs need no library,
     * only this extra; a browser without them ignores it and opens the page itself.
     */
    static Intent browserIntent(Uri url) {
        Intent intent = new Intent(Intent.ACTION_VIEW, url).addCategory(Intent.CATEGORY_BROWSABLE);
        Bundle extras = new Bundle();
        extras.putBinder("android.support.customtabs.extra.SESSION", null);
        intent.putExtras(extras);
        return intent;
    }

    private static String challenge(String verifier) {
        try {
            return base64Url(MessageDigest.getInstance("SHA-256").digest(verifier.getBytes(StandardCharsets.US_ASCII)));
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException(e);
        }
    }

    private static String base64Url(byte[] bytes) {
        return Base64.encodeToString(bytes, Base64.URL_SAFE | Base64.NO_PADDING | Base64.NO_WRAP);
    }

    /** A page to load in the WebView: a form POST when postData is set, a plain GET otherwise. */
    static final class Next {

        final String url;

        @Nullable
        final byte[] postData;

        Next(String url, @Nullable byte[] postData) {
            this.url = url;
            this.postData = postData;
        }
    }
}
