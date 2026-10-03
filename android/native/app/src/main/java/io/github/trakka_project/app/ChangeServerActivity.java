package io.github.trakka_project.app;

import android.app.Activity;
import android.content.Intent;
import android.os.Bundle;

/**
 * Target of the launcher shortcut "Changer de serveur" (res/xml/shortcuts.xml): asks for the
 * connect screen, then brings up {@link MainActivity}, which shows it whether it starts now or was
 * already running ({@link MainActivity#onNewIntent}). This works even when the configured server's
 * pages are broken.
 */
public class ChangeServerActivity extends Activity {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        new ServerStore(this).setChangeRequested(true);
        startActivity(new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP));
        finish();
    }
}
