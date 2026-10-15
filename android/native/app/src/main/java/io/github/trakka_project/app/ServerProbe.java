package io.github.trakka_project.app;

import android.text.Html;
import com.getcapacitor.JSObject;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.MalformedURLException;
import java.net.SocketTimeoutException;
import java.net.URL;
import java.net.UnknownHostException;
import java.util.regex.Matcher;
import java.util.regex.Pattern;
import javax.net.ssl.SSLException;
import org.json.JSONException;
import org.json.JSONObject;

/**
 * Checks a server address before the app switches to it, so that the connect screen can explain a
 * mistake instead of showing a broken page: does the address answer over HTTPS, with a certificate
 * the phone trusts, and is it Trakka? Goes through the app's network security config, like the
 * WebView.
 */
final class ServerProbe {

    private static final int TIMEOUT_MS = 10_000;

    // The SSO button of templates/login.html, for a server older than GET /auth/methods.
    private static final Pattern SSO_BUTTON = Pattern.compile("id=\"oidc-login-link\"[^>]*>\\s*Se connecter avec\\s+([^<]*?)\\s*</a>");

    private ServerProbe() {}

    /**
     * {server, reachable, trakka, status, redirectHost} when the server answered, {server,
     * reachable: false, reason} when it could not be reached.
     */
    static JSObject check(String origin) {
        JSObject result = new JSObject();
        result.put("server", origin);
        HttpURLConnection connection = null;
        try {
            connection = (HttpURLConnection) new URL(origin + "/manifest.json").openConnection();
            connection.setInstanceFollowRedirects(false);
            connection.setConnectTimeout(TIMEOUT_MS);
            connection.setReadTimeout(TIMEOUT_MS);
            connection.setRequestProperty("Accept", "application/manifest+json, application/json");
            int status = connection.getResponseCode();
            result.put("reachable", true);
            result.put("status", status);
            // static/manifest.json names the app. An authentication proxy in front of Trakka answers
            // with a redirect to its portal instead, which the connect screen lets the user accept.
            result.put("trakka", status == 200 && isTrakkaManifest(connection));
            String redirectHost = redirectHost(origin, connection.getHeaderField("Location"));
            if (redirectHost != null) {
                result.put("redirectHost", redirectHost);
            }
        } catch (UnknownHostException e) {
            unreachable(result, "unknown_host");
        } catch (SSLException e) {
            unreachable(result, "tls");
        } catch (SocketTimeoutException e) {
            unreachable(result, "timeout");
        } catch (IOException e) {
            unreachable(result, "unreachable");
        } finally {
            if (connection != null) {
                connection.disconnect();
            }
        }
        return result;
    }

    /**
     * The sign-in methods the server offers, for the app's native sign-in screen: {server,
     * reachable, known: true, password, oidc, providerName, registration} as GET /auth/methods
     * reports them, {server, reachable, known: false, status, redirectHost} when the server
     * answers something else (an authentication proxy's portal, for one), {server, reachable:
     * false, reason} when it could not be reached.
     */
    static JSObject authMethods(String origin) {
        JSObject result = new JSObject();
        result.put("server", origin);
        try {
            HttpURLConnection connection = open(origin + "/auth/methods", "application/json");
            try {
                int status = connection.getResponseCode();
                result.put("reachable", true);
                if (status == 200) {
                    try (InputStream in = connection.getInputStream()) {
                        JSONObject methods = new JSONObject(Streams.readUtf8(in, 16 * 1024));
                        return known(
                            result,
                            methods.optBoolean("password"),
                            methods.optBoolean("oidc"),
                            methods.optString("oidc_provider_name"),
                            methods.optBoolean("registration")
                        );
                    } catch (JSONException e) {
                        // Not Trakka's answer: a portal's page, most likely.
                        return unknown(result, origin, connection, status);
                    }
                }
                if (status != 404) {
                    return unknown(result, origin, connection, status);
                }
            } finally {
                connection.disconnect();
            }
            // A Trakka server older than /auth/methods: read its sign-in page instead.
            connection = open(origin + "/auth/login", "text/html");
            try {
                int status = connection.getResponseCode();
                if (status != 200) {
                    return unknown(result, origin, connection, status);
                }
                String page;
                try (InputStream in = connection.getInputStream()) {
                    page = Streams.readUtf8(in, 256 * 1024);
                }
                Matcher sso = SSO_BUTTON.matcher(page);
                boolean oidc = sso.find();
                String providerName = oidc ? Html.fromHtml(sso.group(1), Html.FROM_HTML_MODE_LEGACY).toString().trim() : "";
                return known(
                    result,
                    page.contains("action=\"/auth/login\""),
                    oidc,
                    "SSO".equals(providerName) ? "" : providerName,
                    page.contains("/auth/login?mode=register")
                );
            } finally {
                connection.disconnect();
            }
        } catch (UnknownHostException e) {
            unreachable(result, "unknown_host");
        } catch (SSLException e) {
            unreachable(result, "tls");
        } catch (SocketTimeoutException e) {
            unreachable(result, "timeout");
        } catch (IOException e) {
            unreachable(result, "unreachable");
        }
        return result;
    }

    private static HttpURLConnection open(String url, String accept) throws IOException {
        HttpURLConnection connection = (HttpURLConnection) new URL(url).openConnection();
        connection.setInstanceFollowRedirects(false);
        connection.setConnectTimeout(TIMEOUT_MS);
        connection.setReadTimeout(TIMEOUT_MS);
        connection.setRequestProperty("Accept", accept);
        return connection;
    }

    private static JSObject known(JSObject result, boolean password, boolean oidc, String providerName, boolean registration) {
        result.put("known", true);
        result.put("password", password);
        result.put("oidc", oidc);
        result.put("providerName", providerName == null ? "" : providerName);
        result.put("registration", registration);
        return result;
    }

    private static JSObject unknown(JSObject result, String origin, HttpURLConnection connection, int status) {
        result.put("known", false);
        result.put("status", status);
        String redirectHost = redirectHost(origin, connection.getHeaderField("Location"));
        if (redirectHost != null) {
            result.put("redirectHost", redirectHost);
        }
        return result;
    }

    private static boolean isTrakkaManifest(HttpURLConnection connection) {
        try (InputStream in = connection.getInputStream()) {
            JSONObject manifest = new JSONObject(Streams.readUtf8(in, 64 * 1024));
            return "Trakka".equals(manifest.optString("short_name")) || "Trakka".equals(manifest.optString("name"));
        } catch (IOException | JSONException e) {
            return false;
        }
    }

    private static String redirectHost(String origin, String location) {
        if (location == null) {
            return null;
        }
        try {
            return new URL(new URL(origin), location).getHost();
        } catch (MalformedURLException e) {
            return null;
        }
    }

    private static void unreachable(JSObject result, String reason) {
        result.put("reachable", false);
        result.put("reason", reason);
    }
}
