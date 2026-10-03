package io.github.trakka_project.app;

import com.getcapacitor.JSObject;
import java.io.IOException;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.MalformedURLException;
import java.net.SocketTimeoutException;
import java.net.URL;
import java.net.UnknownHostException;
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
