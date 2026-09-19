#import <Cocoa/Cocoa.h>
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <sys/file.h>
#include <sys/stat.h>
#include <unistd.h>

static NSError *launcherError(NSString *description) {
    return [NSError errorWithDomain:@"org.tesseras.tailchrome.helper.user"
                                code:1
                            userInfo:@{NSLocalizedDescriptionKey: description}];
}

static NSError *launcherRecoveryError(NSString *description, NSURL *recoveryURL, NSError *underlyingError) {
    NSString *message = description;
    if (underlyingError != nil) {
        message = [message stringByAppendingFormat:@" %@", underlyingError.localizedDescription];
    }
    if (recoveryURL != nil) {
        message = [message stringByAppendingFormat:@" Recovery copy retained at %@.", recoveryURL.path];
    }
    return launcherError(message);
}

static NSString *userHomeDirectoryPath(void) {
#ifdef TAILCHROME_LAUNCHER_TEST
    const char *testPath = getenv("TAILCHROME_TEST_APPLICATIONS_PATH");
    if (testPath != NULL && testPath[0] != '\0') {
        return [[NSString stringWithUTF8String:testPath] stringByDeletingLastPathComponent];
    }
#endif
    return NSHomeDirectory();
}

static NSString *applicationsDirectoryPath(void) {
    return [userHomeDirectoryPath() stringByAppendingPathComponent:@"Applications"];
}

static BOOL pathIsSymlink(NSString *path) {
    struct stat info;
    return lstat(path.fileSystemRepresentation, &info) == 0 && S_ISLNK(info.st_mode);
}

static BOOL validateStableBundle(NSURL *bundleURL, NSError **error) {
    struct stat info;
    if (lstat(bundleURL.path.fileSystemRepresentation, &info) != 0) {
        if (errno == ENOENT) return YES;
        if (error) *error = launcherError(@"The existing application could not be inspected.");
        return NO;
    }
    NSURL *plistURL = [bundleURL URLByAppendingPathComponent:@"Contents/Info.plist"];
    NSDictionary *metadata = [NSDictionary dictionaryWithContentsOfURL:plistURL];
    id bundleIdentifier = metadata[@"CFBundleIdentifier"];
    if (!S_ISDIR(info.st_mode) || info.st_uid != geteuid() ||
        ![bundleIdentifier isKindOfClass:[NSString class]] ||
        ![(NSString *)bundleIdentifier isEqualToString:@"org.tesseras.tailchrome.helper.user"]) {
        if (error) *error = launcherError(@"The existing application is not a Tailchrome helper app owned by this user. Move it before installing.");
        return NO;
    }
    return YES;
}

static int acquireInstallLock(NSError **error) {
    NSString *supportPath = [userHomeDirectoryPath() stringByAppendingPathComponent:@"Library/Application Support/Tailchrome"];
    NSFileManager *fileManager = [NSFileManager defaultManager];
    if (![fileManager createDirectoryAtPath:supportPath
                withIntermediateDirectories:YES attributes:nil error:error]) {
        return -1;
    }
    NSString *lockPath = [supportPath stringByAppendingPathComponent:@"tailchrome-install.lock"];
    int openFlags = O_CREAT | O_RDWR;
#ifdef O_NOFOLLOW
    openFlags |= O_NOFOLLOW;
#endif
    int descriptor = open(lockPath.fileSystemRepresentation, openFlags, 0600);
    if (descriptor < 0) {
        if (error) {
            *error = launcherError([NSString stringWithFormat:@"Could not open install lock: %s", strerror(errno)]);
        }
        return -1;
    }
    if (flock(descriptor, LOCK_EX) != 0) {
        if (error) {
            *error = launcherError([NSString stringWithFormat:@"Could not acquire install lock: %s", strerror(errno)]);
        }
        close(descriptor);
        return -1;
    }
    struct stat lockInfo;
    if (fstat(descriptor, &lockInfo) != 0 || !S_ISREG(lockInfo.st_mode) || lockInfo.st_uid != geteuid()) {
        if (error) {
            *error = launcherError(@"The Tailchrome install lock is not a regular file owned by this user.");
        }
        flock(descriptor, LOCK_UN);
        close(descriptor);
        return -1;
    }
    if (fchmod(descriptor, 0600) != 0) {
        if (error) {
            *error = launcherError([NSString stringWithFormat:@"Could not secure install lock: %s", strerror(errno)]);
        }
        flock(descriptor, LOCK_UN);
        close(descriptor);
        return -1;
    }
    return descriptor;
}

static NSURL *stageStableBundle(NSURL *sourceURL, NSURL *destinationURL, NSURL **backupURL, NSError **error) {
    NSString *applicationsPath = applicationsDirectoryPath();
    NSURL *applicationsURL = [NSURL fileURLWithPath:applicationsPath isDirectory:YES];
    NSString *temporaryName = [NSString stringWithFormat:@".Tailchrome Helper.%@.app", [NSUUID UUID].UUIDString];
    NSURL *temporaryURL = [applicationsURL URLByAppendingPathComponent:temporaryName isDirectory:YES];
    NSFileManager *fileManager = [NSFileManager defaultManager];

    if (pathIsSymlink(applicationsPath)) {
        if (error) *error = launcherError(@"The stable Tailchrome application directory must not be a symbolic link.");
        return nil;
    }
    if (![fileManager createDirectoryAtURL:applicationsURL
                withIntermediateDirectories:YES attributes:nil error:error]) {
        return nil;
    }
    if (pathIsSymlink(applicationsPath) || pathIsSymlink(destinationURL.path)) {
        if (error) *error = launcherError(@"The stable Tailchrome application path must not be a symbolic link.");
        return nil;
    }
    if (![fileManager copyItemAtURL:sourceURL toURL:temporaryURL error:error]) {
        return nil;
    }

    BOOL hadDestination = [fileManager fileExistsAtPath:destinationURL.path];
    NSURL *newBackupURL = [applicationsURL URLByAppendingPathComponent:
        [NSString stringWithFormat:@".Tailchrome Helper.previous.%@.app", [NSUUID UUID].UUIDString]
        isDirectory:YES];
    if (hadDestination) {
        // NSFileManager performs the replacement as one filesystem operation.
        // Keep its backup instead of moving the old app away first: the stable
        // path remains valid throughout activation and registration.
        NSURL *resultURL = nil;
        NSError *replaceError = nil;
        if (![fileManager replaceItemAtURL:destinationURL
                             withItemAtURL:temporaryURL
                            backupItemName:newBackupURL.lastPathComponent
                                  options:NSFileManagerItemReplacementWithoutDeletingBackupItem
                           resultingItemURL:&resultURL
                                     error:&replaceError]) {
            if (error) *error = replaceError;
            return nil;
        }
        if (resultURL == nil) {
            resultURL = destinationURL;
        }
        if (backupURL) *backupURL = newBackupURL;
        return resultURL;
    }
    NSError *moveError = nil;
    if (![fileManager moveItemAtURL:temporaryURL toURL:destinationURL error:&moveError]) {
        NSError *cleanupError = nil;
        if (![fileManager removeItemAtURL:temporaryURL error:&cleanupError] && error) {
            *error = launcherRecoveryError(@"Bundle activation failed and the staging copy could not be removed.", temporaryURL, cleanupError);
        } else if (error) {
            *error = moveError;
        }
        return nil;
    }
    if (backupURL) *backupURL = nil;
    return destinationURL;
}

/*
 * Keep this separate from stageStableBundle so rollback uses the same atomic
 * exchange primitive. The failed new app gets its own retained name while the
 * old app is returned to the stable path.
 */
static BOOL restoreStableBundle(NSURL *destinationURL, NSURL *backupURL, NSError **error) {
    NSFileManager *fileManager = [NSFileManager defaultManager];
    NSURL *failedURL = [[backupURL URLByDeletingLastPathComponent]
        URLByAppendingPathComponent:[NSString stringWithFormat:@".Tailchrome Helper.failed.%@.app", [NSUUID UUID].UUIDString]
                           isDirectory:YES];
    NSURL *resultURL = nil;
    NSError *replaceError = nil;
    if (![fileManager replaceItemAtURL:destinationURL
                         withItemAtURL:backupURL
                        backupItemName:failedURL.lastPathComponent
                              options:NSFileManagerItemReplacementWithoutDeletingBackupItem
                       resultingItemURL:&resultURL
                                 error:&replaceError]) {
        if (error) {
            *error = launcherRecoveryError(@"The previous stable helper could not be restored atomically.", backupURL, replaceError);
        }
        return NO;
    }
    NSError *cleanupError = nil;
    if (![fileManager removeItemAtURL:failedURL error:&cleanupError]) {
        if (error) {
            *error = launcherRecoveryError(@"The previous stable helper was restored, but the failed replacement could not be removed.", failedURL, cleanupError);
        }
        return NO;
    }
    return YES;
}

static BOOL installHelper(NSURL *bundleURL, NSError **error) {
    NSURL *helperURL = [bundleURL URLByAppendingPathComponent:@"Contents/MacOS/tailscale-browser-ext"];
    NSTask *task = [[NSTask alloc] init];
    task.executableURL = helperURL;
    task.arguments = @[@"install", @"--binary-path", helperURL.path];
    task.standardInput = [NSFileHandle fileHandleWithNullDevice];
    if (![task launchAndReturnError:error]) {
        return NO;
    }
    [task waitUntilExit];
    if (task.terminationReason != NSTaskTerminationReasonExit || task.terminationStatus != 0) {
        if (error && *error == nil) {
            *error = launcherError([NSString stringWithFormat:@"Helper registration failed with exit status %d.", task.terminationStatus]);
        }
        return NO;
    }
    return YES;
}

static BOOL installStableHelper(NSURL *sourceURL, NSError **error) {
    NSString *applicationsPath = applicationsDirectoryPath();
    NSURL *applicationsURL = [NSURL fileURLWithPath:applicationsPath isDirectory:YES];
    NSURL *destinationURL = [applicationsURL URLByAppendingPathComponent:@"Tailchrome Helper.app" isDirectory:YES];
    int lock = acquireInstallLock(error);
    if (lock < 0) return NO;

    NSFileManager *fileManager = [NSFileManager defaultManager];
    BOOL installed = NO;
    NSURL *backupURL = nil;
    if (pathIsSymlink(applicationsPath) || pathIsSymlink(destinationURL.path)) {
        if (error) *error = launcherError(@"The stable Tailchrome application path must not be a symbolic link.");
        flock(lock, LOCK_UN);
        close(lock);
        return NO;
    }
    if (!validateStableBundle(destinationURL, error)) {
        flock(lock, LOCK_UN);
        close(lock);
        return NO;
    }
    BOOL alreadyStable = [[[sourceURL URLByStandardizingPath] path] isEqualToString:
        [[destinationURL URLByStandardizingPath] path]];
    if (alreadyStable) {
        installed = installHelper(destinationURL, error);
    } else {
        NSURL *stagedURL = stageStableBundle(sourceURL, destinationURL, &backupURL, error);
        if (stagedURL != nil) {
            NSError *registrationError = nil;
            installed = installHelper(stagedURL, &registrationError);
            if (installed) {
                if (backupURL != nil) {
                    NSError *cleanupError = nil;
                    if (![fileManager removeItemAtURL:backupURL error:&cleanupError]) {
                        if (error) {
                            *error = launcherRecoveryError(@"Helper registration succeeded, but the previous stable app could not be removed.", backupURL, cleanupError);
                        }
                        installed = NO;
                    }
                }
            } else {
                if (backupURL != nil) {
                    NSError *restoreError = nil;
                    if (!restoreStableBundle(destinationURL, backupURL, &restoreError)) {
                        if (error) {
                            *error = launcherRecoveryError(@"Helper registration failed and the previous stable app could not be restored.", backupURL, restoreError ?: registrationError);
                        }
                    } else if (error) {
                        *error = registrationError;
                    }
                } else {
                    NSError *removeError = nil;
                    if (![fileManager removeItemAtURL:destinationURL error:&removeError]) {
                        if (error) {
                            *error = launcherRecoveryError(@"Helper registration failed and the new stable app could not be removed.", destinationURL, removeError ?: registrationError);
                        }
                    } else if (error) {
                        *error = registrationError;
                    }
                }
            }
        }
    }
    flock(lock, LOCK_UN);
    close(lock);
    return installed;
}

#ifndef TAILCHROME_LAUNCHER_TEST
int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        NSError *error = nil;
        BOOL installed = installStableHelper([NSBundle mainBundle].bundleURL, &error);
        if (error) {
            NSLog(@"Helper setup failed: %@", error);
        }
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = installed ? @"Setup is complete" : @"Setup could not finish";
        NSString *failureDetails = error.localizedDescription;
        if (failureDetails.length == 0) {
            failureDetails = @"The helper could not complete installation.";
        }
        alert.informativeText = installed
            ? @"Return to your browser and open Tailchrome."
            : [NSString stringWithFormat:@"%@ Close your browsers and try again. If the app is incomplete, download Tailchrome Helper again from GitHub Releases.", failureDetails];
        alert.alertStyle = installed ? NSAlertStyleInformational : NSAlertStyleCritical;
        [alert addButtonWithTitle:@"OK"];
        [NSApp activateIgnoringOtherApps:YES];
        [alert runModal];
        return installed ? 0 : 1;
    }
}
#endif
