package io.github.trakka_project.app;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Context;
import android.os.Build;

/**
 * The notification channels of task reminders, which Trakka's pages schedule on the phone through
 * Capacitor's LocalNotifications plugin (static/js/local-reminders.js) and post on one channel or
 * the other according to the account's "Activer les vibrations des notifications" setting.
 *
 * <p>Two channels rather than one, because Android fixes a channel's sound and vibration once it
 * is created: the app can't switch them on a single channel. They mirror what a Web Push reminder
 * does in a browser (static/sw.js): a short double vibration with the phone's notification sound,
 * or neither. Created at every start, which only refreshes their names (in the phone's language);
 * the user's own changes to them in Android's settings are kept.
 */
final class ReminderChannels {

    /** Must match static/js/local-reminders.js. */
    static final String VIBRATING = "trakka_reminders";
    static final String QUIET = "trakka_reminders_quiet";

    // internal/handlers/push.go's notificationVibratePattern, with Android's leading delay.
    private static final long[] VIBRATION_PATTERN = { 0, 200, 100, 200 };

    private ReminderChannels() {}

    static void create(Context context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return;
        }
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (manager == null) {
            return;
        }

        NotificationChannel vibrating = new NotificationChannel(
            VIBRATING,
            context.getString(R.string.channel_reminders),
            NotificationManager.IMPORTANCE_DEFAULT
        );
        vibrating.setDescription(context.getString(R.string.channel_reminders_description));
        vibrating.enableVibration(true);
        vibrating.setVibrationPattern(VIBRATION_PATTERN);
        manager.createNotificationChannel(vibrating);

        NotificationChannel quiet = new NotificationChannel(
            QUIET,
            context.getString(R.string.channel_reminders_quiet),
            NotificationManager.IMPORTANCE_LOW
        );
        quiet.setDescription(context.getString(R.string.channel_reminders_quiet_description));
        quiet.setSound(null, null);
        quiet.enableVibration(false);
        manager.createNotificationChannel(quiet);
    }
}
