// winch-notify: winch's own macOS notification bundle.
//
// It exists because a macOS notification is attributed to an APP, not to a
// process. A bare CLI has no bundle identifier, so UNUserNotificationCenter
// refuses it outright; shelling out to osascript borrows Script Editor's
// identity (which is why clicking one opened Script Editor); and
// terminal-notifier borrows terminal-notifier's, and asks every winch user to
// install it. This bundle is how the notification gets winch's own name, icon
// and click behaviour with nothing for the user to install.
//
// It runs in two modes, and the second is not optional:
//
//   post   argv carries a notification; deliver it and exit.
//   serve  argv is empty because LAUNCHSERVICES started us — the user clicked
//          a banner and macOS relaunched the app to deliver the response.
//          The posting process is long gone; this is the only way a
//          short-lived notifier can act on a click at all.
//
// Registration: the modern API only talks to apps LaunchServices knows, and
// nix store paths are never scanned, so the bundle must be lsregister'd once.
// That is what `winch notify-install` does. Verified 2026-09-01: without it
// requestAuthorization returns "Notifications are not allowed for this
// application" and the app never appears in System Settings; with it the app
// registers, authorises and delivers. This is also the whole explanation for
// kitty (modern API, nix store, never registers) versus terminal-notifier
// (deprecated NSUserNotification API, nix store, works).

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <UserNotifications/UserNotifications.h>

// Keys we stash in the notification's userInfo so the RELAUNCHED process can
// reconstruct what to do. It has no argv and no memory of the sender.
static NSString *const kWinch  = @"winch";  // absolute path to the winch binary
static NSString *const kSocket = @"socket"; // tmux socket the pane lives on
static NSString *const kPane   = @"pane";   // %N to jump to
static NSString *const kBundle = @"bundle"; // terminal to raise, e.g. net.kovidgoyal.kitty

@interface Delegate : NSObject <UNUserNotificationCenterDelegate>
@property(nonatomic) BOOL done;
@property(nonatomic) int rc;
@end

@implementation Delegate

// The click. Jump tmux to the pane FIRST, then raise the terminal: doing it
// the other way round shows you the old pane for a frame before it moves.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
    didReceiveNotificationResponse:(UNNotificationResponse *)response
             withCompletionHandler:(void (^)(void))completionHandler {
	NSDictionary *info = response.notification.request.content.userInfo;
	NSString *winch = info[kWinch], *sock = info[kSocket], *pane = info[kPane];

	if (winch.length && sock.length && pane.length) {
		NSTask *t = [NSTask new];
		t.executableURL = [NSURL fileURLWithPath:winch];
		t.arguments = @[ @"-S", sock, @"focus", pane ];
		NSError *err = nil;
		if (![t launchAndReturnError:&err]) {
			fprintf(stderr, "winch-notify: focus: %s\n",
			        [[err localizedDescription] UTF8String]);
		} else {
			[t waitUntilExit];
		}
	}
	// Raising is best-effort and deliberately not fatal: the jump already
	// happened, and a terminal we cannot name is better left alone than
	// guessed at.
	NSString *bundle = info[kBundle];
	if (bundle.length) {
		[[NSWorkspace sharedWorkspace]
		    launchAppWithBundleIdentifier:bundle
		                          options:NSWorkspaceLaunchDefault
		   additionalEventParamDescriptor:nil
		                 launchIdentifier:NULL];
	}
	completionHandler();
	self.done = YES;
	self.rc = 0;
}

// Show the banner even when winch's own terminal is frontmost. macOS
// suppresses foreground notifications by default; winch has already decided
// whether you can see the pane (it suppresses per client, using tmux's own
// focus reporting), so a second, coarser opinion here would only drop alerts
// winch had judged worth sending.
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))h {
	h(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionSound);
}
@end

static NSString *arg(NSArray<NSString *> *a, NSString *flag) {
	NSUInteger i = [a indexOfObject:flag];
	if (i == NSNotFound || i + 1 >= a.count) return nil;
	return a[i + 1];
}

static void pump(Delegate *d, NSTimeInterval seconds) {
	NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:seconds];
	while (!d.done && [deadline timeIntervalSinceNow] > 0) {
		[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode
		                         beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
	}
}

int main(int argc, const char *argv[]) {
	@autoreleasepool {
		if (![[NSBundle mainBundle] bundleIdentifier]) {
			fprintf(stderr, "winch-notify: no bundle identifier — run the binary "
			                "inside winch-notify.app, not on its own\n");
			return 2;
		}
		[NSApplication sharedApplication];

		UNUserNotificationCenter *c = [UNUserNotificationCenter currentNotificationCenter];
		Delegate *d = [Delegate new];
		c.delegate = d;

		NSArray<NSString *> *a = [[NSProcessInfo processInfo] arguments];
		NSString *title = arg(a, @"--title"), *body = arg(a, @"--body");

		// No --title: LaunchServices started us for a click. Sit on the run
		// loop long enough for the delegate to be handed the response.
		if (!title.length) {
			pump(d, 10);
			return d.done ? d.rc : 0;
		}

		// Ask the system what it thinks of us BEFORE requesting anything.
		// The three states are genuinely different problems and the user can
		// only act on one of them, so guessing between them in an error
		// message is worse than useless:
		//
		//   notDetermined  never registered      -> winch notify-install
		//   denied         registered, toggled off -> System Settings
		//   authorized     fine
		//
		// requestAuthorization simply never calls back in the denied case,
		// which is why this is read up front rather than inferred from a
		// timeout.
		__block UNAuthorizationStatus status = UNAuthorizationStatusNotDetermined;
		__block BOOL haveStatus = NO;
		[c getNotificationSettingsWithCompletionHandler:^(UNNotificationSettings *s) {
			status = s.authorizationStatus;
			haveStatus = YES;
		}];
		NSDate *sd = [NSDate dateWithTimeIntervalSinceNow:3];
		while (!haveStatus && [sd timeIntervalSinceNow] > 0) {
			[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode
			                         beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
		}
		if (haveStatus && status == UNAuthorizationStatusDenied) {
			fprintf(stderr, "winch-notify: winch is registered but switched OFF.\n"
			                "Enable it in System Settings > Notifications > winch.\n");
			return 4;
		}

		__block BOOL asked = NO;
		__block BOOL ok = NO;
		[c requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionSound)
			completionHandler:^(BOOL granted, NSError *err) {
				ok = granted;
				if (!granted) {
					fprintf(stderr, "winch-notify: not authorised: %s\n",
					        err ? [[err localizedDescription] UTF8String]
					            : "no permission (run `winch notify-install`, then enable "
					              "winch in System Settings > Notifications)");
				}
				asked = YES;
			}];
		NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:10];
		while (!asked && [deadline timeIntervalSinceNow] > 0) {
			[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode
			                         beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
		}
		if (!asked) {
			fprintf(stderr, "winch-notify: authorisation never answered. Register the "
			                "bundle with `winch notify-install`, then enable winch in "
			                "System Settings > Notifications.\n");
			return 3;
		}
		if (!ok) return 4;

		UNMutableNotificationContent *content = [UNMutableNotificationContent new];
		content.title = title;
		content.body = body ?: @"";
		content.sound = [UNNotificationSound defaultSound];

		NSMutableDictionary *info = [NSMutableDictionary dictionary];
		for (NSString *k in @[ kWinch, kSocket, kPane, kBundle ]) {
			NSString *v = arg(a, [@"--" stringByAppendingString:k]);
			if (v.length) info[k] = v;
		}
		content.userInfo = info;

		// One identifier per PANE, so a second notification for an agent
		// REPLACES the first instead of stacking. Five reminders that the
		// same agent is still waiting is noise, not urgency.
		NSString *ident = info[kPane] ?: [[NSUUID UUID] UUIDString];
		__block BOOL sent = NO;
		[c addNotificationRequest:[UNNotificationRequest requestWithIdentifier:ident
		                                                              content:content
		                                                              trigger:nil]
		    withCompletionHandler:^(NSError *e) {
			    if (e) fprintf(stderr, "winch-notify: %s\n",
			                   [[e localizedDescription] UTF8String]);
			    else d.rc = 0;
			    sent = YES;
		    }];
		NSDate *d2 = [NSDate dateWithTimeIntervalSinceNow:5];
		while (!sent && [d2 timeIntervalSinceNow] > 0) {
			[[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode
			                         beforeDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
		}
		return sent ? d.rc : 5;
	}
}
