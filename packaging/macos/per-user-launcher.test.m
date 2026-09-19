#define TAILCHROME_LAUNCHER_TEST
#import "per-user-launcher.m"
#include <assert.h>
#include <limits.h>
#include <string.h>

static void writeHelper(NSURL *url, NSString *script, NSNumber *mode) {
    [[NSFileManager defaultManager] removeItemAtURL:url error:NULL];
    assert([[NSFileManager defaultManager] createFileAtPath:url.path
        contents:[script dataUsingEncoding:NSUTF8StringEncoding]
        attributes:@{NSFilePosixPermissions: mode}]);
}

static NSString *absoluteExecutablePath(const char *argv0) {
    char resolved[PATH_MAX];
    assert(realpath(argv0, resolved) != NULL);
    return [NSString stringWithUTF8String:resolved];
}

static int runLockProbe(NSString *markerPath) {
    @autoreleasepool {
        NSError *error = nil;
        int lock = acquireInstallLock(&error);
        if (lock < 0) return 1;
        BOOL created = [[NSFileManager defaultManager] createFileAtPath:markerPath contents:nil attributes:nil];
        flock(lock, LOCK_UN);
        close(lock);
        return created ? 0 : 1;
    }
}

static int runLockHolder(NSString *sourcePath) {
    @autoreleasepool {
        NSError *error = nil;
        BOOL installed = installStableHelper([NSURL fileURLWithPath:sourcePath], &error);
        return installed ? 0 : 1;
    }
}

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc == 3 && strcmp(argv[1], "lock-probe") == 0) {
            return runLockProbe([NSString stringWithUTF8String:argv[2]]);
        }
        if (argc == 3 && strcmp(argv[1], "lock-holder") == 0) {
            return runLockHolder([NSString stringWithUTF8String:argv[2]]);
        }
        NSFileManager *fm = [NSFileManager defaultManager];
        NSURL *root = [NSURL fileURLWithPath:[NSTemporaryDirectory()
            stringByAppendingPathComponent:[NSUUID UUID].UUIDString]];
        NSURL *bundle = [root URLByAppendingPathComponent:@"A user's Downloads/Tailchrome Helper.app"];
        NSURL *macOS = [bundle URLByAppendingPathComponent:@"Contents/MacOS"];
        NSURL *applications = [root URLByAppendingPathComponent:@"Applications"];
        assert([fm createDirectoryAtURL:macOS withIntermediateDirectories:YES attributes:nil error:NULL]);
        assert([fm createDirectoryAtURL:applications withIntermediateDirectories:YES attributes:nil error:NULL]);
        assert([@{@"CFBundleIdentifier": @"org.tesseras.tailchrome.helper.user"}
            writeToURL:[bundle URLByAppendingPathComponent:@"Contents/Info.plist"] atomically:YES]);
        NSURL *codeSignature = [bundle URLByAppendingPathComponent:@"Contents/_CodeSignature" isDirectory:YES];
        assert([fm createDirectoryAtURL:codeSignature withIntermediateDirectories:YES attributes:nil error:NULL]);
        NSURL *codeResources = [codeSignature URLByAppendingPathComponent:@"CodeResources"];
        assert([fm createFileAtPath:codeResources.path contents:[NSData data] attributes:nil]);
        setenv("TAILCHROME_TEST_APPLICATIONS_PATH", applications.path.fileSystemRepresentation, 1);
        NSURL *helper = [macOS URLByAppendingPathComponent:@"tailscale-browser-ext"];
        NSError *error = nil;

        assert(!installHelper(bundle, &error));
        assert(error != nil);

        writeHelper(helper, @"#!/bin/bash\n[[ $# == 3 && $1 == install && $2 == --binary-path && $3 == */tailscale-browser-ext ]] || exit 9\n! read -r line\n", @0755);
        assert(installHelper(bundle, NULL));

        NSError *stableError = nil;
        NSURL *stableBundle = [applications URLByAppendingPathComponent:@"Tailchrome Helper.app"];
        assert([fm createDirectoryAtURL:stableBundle withIntermediateDirectories:YES attributes:nil error:NULL]);
        NSURL *unrelatedFile = [stableBundle URLByAppendingPathComponent:@"keep.txt"];
        assert([fm createFileAtPath:unrelatedFile.path contents:[NSData data] attributes:nil]);
        assert(!installStableHelper(bundle, &stableError));
        assert([fm fileExistsAtPath:unrelatedFile.path]);
        assert([fm removeItemAtURL:stableBundle error:NULL]);
        stableError = nil;
        NSURL *malformedContents = [stableBundle URLByAppendingPathComponent:@"Contents" isDirectory:YES];
        assert([fm createDirectoryAtURL:malformedContents withIntermediateDirectories:YES attributes:nil error:NULL]);
        assert([@{ @"CFBundleIdentifier": @42 }
            writeToURL:[stableBundle URLByAppendingPathComponent:@"Contents/Info.plist"] atomically:YES]);
        assert(!installStableHelper(bundle, &stableError));
        assert([stableError.localizedDescription containsString:@"not a Tailchrome helper app"]);
        assert([fm removeItemAtURL:stableBundle error:NULL]);
        stableError = nil;
        assert(installStableHelper(bundle, &stableError));
        NSURL *stableHelper = [stableBundle URLByAppendingPathComponent:@"Contents/MacOS/tailscale-browser-ext"];
        assert([fm isExecutableFileAtPath:stableHelper.path]);
        assert([fm fileExistsAtPath:[stableBundle.path stringByAppendingPathComponent:@"Contents/_CodeSignature/CodeResources"]]);
        NSDictionary *beforeRepair = [fm attributesOfItemAtPath:stableHelper.path error:NULL];
        assert(installStableHelper(stableBundle, &stableError));
        NSDictionary *afterRepair = [fm attributesOfItemAtPath:stableHelper.path error:NULL];
        assert([beforeRepair[NSFileSystemFileNumber] isEqual:afterRepair[NSFileSystemFileNumber]]);

        writeHelper(helper, @"#!/bin/bash\nexit 7\n", @0755);
        assert(!installStableHelper(bundle, &stableError));
        NSData *stableContents = [fm contentsAtPath:stableHelper.path];
        assert(stableContents != nil);
        assert(![stableContents isEqualToData:[fm contentsAtPath:helper.path]]);

        NSString *applicationsShellPath = [applications.path stringByReplacingOccurrencesOfString:@"'" withString:@"'\\''"];
        writeHelper(helper, [NSString stringWithFormat:@"#!/bin/bash\nchmod 0500 '%@'\nexit 7\n", applicationsShellPath], @0755);
        stableError = nil;
        assert(!installStableHelper(bundle, &stableError));
        assert([stableError.localizedDescription containsString:@"Recovery copy retained at"]);
        assert([[fm contentsOfDirectoryAtURL:applications includingPropertiesForKeys:nil options:0 error:NULL]
            filteredArrayUsingPredicate:[NSPredicate predicateWithBlock:^BOOL(NSURL *url, NSDictionary *_) {
                return [url.lastPathComponent hasPrefix:@".Tailchrome Helper.previous."];
            }]].count == 1);
        assert(chmod(applications.path.fileSystemRepresentation, 0700) == 0);

        writeHelper(helper, @"#!/bin/bash\nexit 7\n", @0755);
        assert(!installHelper(bundle, NULL));

        writeHelper(helper, @"#!/bin/bash\nexit 0\n", @0644);
        assert(!installHelper(bundle, NULL));
        [fm removeItemAtURL:stableBundle error:NULL];
        assert([fm createSymbolicLinkAtURL:stableBundle withDestinationURL:bundle error:NULL]);
        assert(!installStableHelper(bundle, &stableError));
        assert(!installStableHelper(stableBundle, &stableError));
        assert([fm removeItemAtURL:stableBundle error:NULL]);

        NSString *startedMarker = [root.path stringByAppendingPathComponent:@"registration-started"];
        NSString *probeMarker = [root.path stringByAppendingPathComponent:@"lock-probe-acquired"];
        writeHelper(helper, [NSString stringWithFormat:@"#!/bin/bash\nprintf started > '%@'\nsleep 2\nexit 0\n", startedMarker], @0755);
        NSTask *holder = [[NSTask alloc] init];
        holder.launchPath = absoluteExecutablePath(argv[0]);
        holder.arguments = @[@"lock-holder", bundle.path];
        assert([holder launchAndReturnError:&stableError]);
        for (NSUInteger attempt = 0; attempt < 40 && ![fm fileExistsAtPath:startedMarker]; attempt++) {
            [NSThread sleepForTimeInterval:0.05];
        }
        assert([fm fileExistsAtPath:startedMarker]);
        NSTask *probe = [[NSTask alloc] init];
        probe.launchPath = absoluteExecutablePath(argv[0]);
        probe.arguments = @[@"lock-probe", probeMarker];
        assert([probe launchAndReturnError:&stableError]);
        [NSThread sleepForTimeInterval:0.25];
        assert(![fm fileExistsAtPath:probeMarker]);
        [holder waitUntilExit];
        [probe waitUntilExit];
        assert(holder.terminationStatus == 0);
        assert(probe.terminationStatus == 0);
        assert([fm fileExistsAtPath:probeMarker]);

        NSString *lockPath = [root.path stringByAppendingPathComponent:@"Library/Application Support/Tailchrome/tailchrome-install.lock"];
        assert([fm fileExistsAtPath:lockPath]);
        NSDictionary *lockAttributes = [fm attributesOfItemAtPath:lockPath error:NULL];
        assert(([lockAttributes[NSFilePosixPermissions] unsignedShortValue] & 0777) == 0600);
        unsetenv("TAILCHROME_TEST_APPLICATIONS_PATH");
        assert([fm removeItemAtURL:root error:NULL]);
        puts("Per-user launcher tests passed.");
    }
    return 0;
}
