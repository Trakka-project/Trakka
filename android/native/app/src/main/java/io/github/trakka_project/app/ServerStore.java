package io.github.trakka_project.app;

import android.content.Context;
import android.content.SharedPreferences;
import androidx.annotation.Nullable;
import java.net.IDN;
import java.net.URI;
import java.net.URISyntaxException;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import org.json.JSONArray;
import org.json.JSONException;

/**
 * The Trakka servers this app knows: the one it opens, recently used ones for quick switching, and
 * whether the connect screen was asked for. Stored in the app's private SharedPreferences, never
 * in the WebView, so no page can read or change it except through {@link TrakkaAppPlugin}.
 */
final class ServerStore {

    private static final String PREFS = "trakka_servers";
    private static final String KEY_CURRENT = "current";
    private static final String KEY_RECENT = "recent";
    // Set by "Changer de serveur", cleared once a server is chosen or the change is cancelled.
    private static final String KEY_CHANGE_REQUESTED = "change_requested";
    private static final int MAX_RECENT = 5;

    private final SharedPreferences prefs;

    ServerStore(Context context) {
        prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    @Nullable
    String current() {
        return prefs.getString(KEY_CURRENT, null);
    }

    /** Most recently used first, the current server included. */
    List<String> recent() {
        List<String> servers = new ArrayList<>();
        try {
            JSONArray array = new JSONArray(prefs.getString(KEY_RECENT, "[]"));
            for (int i = 0; i < array.length(); i++) {
                servers.add(array.getString(i));
            }
        } catch (JSONException ignored) {
            // A corrupt list is only a convenience lost.
        }
        return servers;
    }

    boolean changeRequested() {
        return prefs.getBoolean(KEY_CHANGE_REQUESTED, false);
    }

    void setChangeRequested(boolean requested) {
        prefs.edit().putBoolean(KEY_CHANGE_REQUESTED, requested).commit();
    }

    void connect(String origin) {
        List<String> servers = recent();
        servers.remove(origin);
        servers.add(0, origin);
        while (servers.size() > MAX_RECENT) {
            servers.remove(servers.size() - 1);
        }
        // commit(), not apply(): the activity restarts right after and must read the new state.
        prefs.edit()
            .putString(KEY_CURRENT, origin)
            .putString(KEY_RECENT, new JSONArray(servers).toString())
            .putBoolean(KEY_CHANGE_REQUESTED, false)
            .commit();
    }

    void forget(String origin) {
        List<String> servers = recent();
        servers.remove(origin);
        SharedPreferences.Editor editor = prefs.edit().putString(KEY_RECENT, new JSONArray(servers).toString());
        if (origin.equals(current())) {
            editor.remove(KEY_CURRENT);
        }
        editor.commit();
    }

    /**
     * The origin (scheme://host[:port]) of a server address typed or scanned by the user, such as
     * "trakka.example.com" or "https://trakka.example.com/?list=3". Trakka is always served from the
     * root of its host, so any path is dropped.
     *
     * @throws InvalidServerException with a reason code the connect screen translates
     */
    static String normalize(@Nullable String input) throws InvalidServerException {
        String value = input == null ? "" : input.trim();
        if (value.isEmpty()) {
            throw new InvalidServerException("empty");
        }
        if (!value.contains("://")) {
            value = "https://" + value;
        }
        URI uri;
        try {
            uri = new URI(value);
        } catch (URISyntaxException e) {
            throw new InvalidServerException("invalid");
        }
        String scheme = uri.getScheme() == null ? "" : uri.getScheme().toLowerCase(Locale.ROOT);
        if (scheme.equals("http")) {
            // Service workers, the Secure session cookie and anything else that makes Trakka work
            // need HTTPS, and the app refuses clear-text traffic (network_security_config.xml).
            throw new InvalidServerException("https_required");
        }
        if (!scheme.equals("https") || uri.getHost() == null || uri.getRawUserInfo() != null) {
            throw new InvalidServerException("invalid");
        }
        String host = uri.getHost();
        try {
            // An IPv6 literal keeps its brackets; a name becomes ASCII (punycode).
            host = (host.startsWith("[") ? host : IDN.toASCII(host)).toLowerCase(Locale.ROOT);
        } catch (IllegalArgumentException e) {
            throw new InvalidServerException("invalid");
        }
        return "https://" + host + (uri.getPort() == -1 || uri.getPort() == 443 ? "" : ":" + uri.getPort());
    }

    static final class InvalidServerException extends Exception {

        final String reason;

        InvalidServerException(String reason) {
            super(reason);
            this.reason = reason;
        }
    }
}
