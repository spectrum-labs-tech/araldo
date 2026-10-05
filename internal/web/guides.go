// SPDX-License-Identifier: AGPL-3.0-or-later

package web

import (
	"html/template"

	"github.com/spectrum-labs-tech/araldo/internal/platform"
)

// appGuide is how to register a developer app on one platform: what to
// choose in its forms, and what goes where in Araldo. docs/operations.md
// ("Connecting platforms") says the same at more length; TestAppGuides
// checks every platform with a developer app has both.
type appGuide struct {
	// Steps are done on the platform's site, in order. "The redirect URI"
	// is the one shown above them.
	Steps []template.HTML
	// ClientID and Confidential say which of the platform's values go in
	// Araldo's two fields.
	ClientID, Confidential template.HTML
	// Notes are what is easy to get wrong, or to be surprised by later.
	Notes []template.HTML
}

// appGuides are the setup guides, by platform.
var appGuides = map[platform.Provider]appGuide{
	platform.X: {
		Steps: []template.HTML{
			`Open your app in a project at developer.x.com (or create one), then <strong>User authentication settings → Set up</strong>.`,
			`App permissions: <strong>Read and write</strong>. Araldo needs no direct messages.`,
			`Request email from users: <strong>off</strong>.`,
			`Type of App: <strong>Web App, Automated App or Bot</strong> (a confidential client: Araldo keeps the secret on its server).`,
			`Callback URI: the redirect URI. Website URL: your product's site, which X shows when people sign in.`,
			`Save, then copy the <strong>OAuth 2.0 Client ID and Client Secret</strong> from <strong>Keys and tokens</strong>.`,
		},
		ClientID:     `the OAuth 2.0 Client ID (not the API key)`,
		Confidential: `the OAuth 2.0 Client Secret`,
		Notes: []template.HTML{
			`X's Free tier posts text only; images and video need the Basic tier.`,
			`Pasting keys instead of signing in? Generate the access token and secret <em>after</em> choosing Read and write: a token keeps the permissions it was made with.`,
		},
	},
	platform.LinkedIn: {
		Steps: []template.HTML{
			`Create an app at linkedin.com/developers/apps. LinkedIn asks for a LinkedIn Page it belongs to.`,
			`<strong>Products</strong>: add <strong>Sign In with LinkedIn using OpenID Connect</strong> and <strong>Share on LinkedIn</strong>.`,
			`<strong>Auth</strong>: add the redirect URI under <strong>Authorized redirect URLs</strong>.`,
			`Copy the <strong>Client ID</strong> and <strong>Primary Client Secret</strong> from the Auth tab.`,
		},
		ClientID:     `the Client ID`,
		Confidential: `the Primary Client Secret`,
		Notes: []template.HTML{
			`Araldo posts to the feed of the member who signs in.`,
			`Tokens last 60 days. LinkedIn gives refresh tokens only to apps it has approved for them, so otherwise the channel says a week ahead when to sign in again.`,
		},
	},
	platform.Threads: {
		Steps: []template.HTML{
			`At developers.facebook.com/apps, create an app with the <strong>Access the Threads API</strong> use case.`,
			`Add the permissions <code>threads_basic</code>, <code>threads_content_publish</code> and <code>threads_manage_insights</code>.`,
			`In the use case's settings, add the redirect URI to the <strong>redirect callback URLs</strong>.`,
			`While the app is in development, add each Threads account you will connect as a <strong>Threads tester</strong> under App roles; they accept in Threads → Settings → Website permissions.`,
		},
		ClientID:     `the <strong>Threads app ID</strong> (not the Meta app ID)`,
		Confidential: `the Threads app secret`,
		Notes: []template.HTML{
			`Threads fetches images from a link to this server, so posts with images need it reachable from the internet.`,
		},
	},
	platform.Facebook: {
		Steps: []template.HTML{
			`At developers.facebook.com/apps, create a <strong>Business</strong> app with <strong>Facebook Login for Business</strong>.`,
			`Request <code>pages_show_list</code>, <code>pages_manage_posts</code> and <code>pages_read_engagement</code>.`,
			`In Facebook Login's settings, add the redirect URI to <strong>Valid OAuth Redirect URIs</strong>.`,
			`Copy the <strong>App ID</strong> and <strong>App secret</strong> from App settings → Basic.`,
		},
		ClientID:     `the App ID`,
		Confidential: `the App secret`,
		Notes: []template.HTML{
			`In development the app works for people with a role on it; posting for anyone else needs Meta's app review.`,
			`One Meta app can serve Instagram too: add it here a second time, as Instagram.`,
		},
	},
	platform.Instagram: {
		Steps: []template.HTML{
			`Use a Meta <strong>Business</strong> app with <strong>Facebook Login for Business</strong> (the Facebook one works).`,
			`Request <code>instagram_basic</code>, <code>instagram_content_publish</code>, <code>pages_show_list</code> and <code>business_management</code>.`,
			`In Facebook Login's settings, add the redirect URI to <strong>Valid OAuth Redirect URIs</strong>.`,
			`Copy the <strong>App ID</strong> and <strong>App secret</strong> from App settings → Basic.`,
		},
		ClientID:     `the App ID`,
		Confidential: `the App secret`,
		Notes: []template.HTML{
			`The Instagram account must be a professional account linked to a Facebook Page.`,
			`Instagram fetches images from a link to this server, so it must be reachable from the internet.`,
			`In development the app works for people with a role on it; for anyone else, Meta's app review.`,
		},
	},
	platform.Pinterest: {
		Steps: []template.HTML{
			`Create an app at developers.pinterest.com/apps.`,
			`Add the redirect URI under the app's <strong>redirect URIs</strong>.`,
			`Copy the <strong>App ID</strong> and <strong>App secret key</strong>.`,
		},
		ClientID:     `the App ID`,
		Confidential: `the App secret key`,
		Notes: []template.HTML{
			`Until Pinterest reviews the app for Standard access it has Trial access, which limits it: the app's page says which.`,
			`Each board you choose becomes a channel, and every pin needs an image.`,
		},
	},
	platform.YouTube: {
		Steps: []template.HTML{
			`In a Google Cloud project, enable the <strong>YouTube Data API v3</strong> (APIs &amp; Services → Library).`,
			`Configure the <strong>OAuth consent screen</strong> (External), adding yourself as a test user while it is in testing.`,
			`<strong>Credentials → Create credentials → OAuth client ID</strong>, type <strong>Web application</strong>, with the redirect URI under <strong>Authorized redirect URIs</strong>.`,
			`Copy its <strong>Client ID</strong> and <strong>Client secret</strong>.`,
		},
		ClientID:     `the Client ID`,
		Confidential: `the Client secret`,
		Notes: []template.HTML{
			`While the consent screen is in testing, Google ends the sign-in after seven days: publish the app to keep channels connected.`,
			`Each upload costs 1,600 of the project's 10,000 daily quota units: about six videos a day until Google raises it.`,
		},
	},
	platform.TikTok: {
		Steps: []template.HTML{
			`Create an app at developers.tiktok.com/apps.`,
			`Add the products <strong>Login Kit</strong> and <strong>Content Posting API</strong>, with the scopes <code>user.info.basic</code> and <code>video.publish</code>.`,
			`In Login Kit, add the redirect URI.`,
			`Copy the <strong>Client key</strong> and <strong>Client secret</strong>.`,
		},
		ClientID:     `the Client key`,
		Confidential: `the Client secret`,
		Notes: []template.HTML{
			`Until TikTok audits the app, it can post only to private accounts, and its posts are private.`,
		},
	},
	"reddit_ads": {
		Steps: []template.HTML{
			`At reddit.com/prefs/apps, <strong>create another app</strong> of type <strong>web app</strong>, with the redirect URI as its redirect uri.`,
			`Make sure the Reddit account has <strong>Ads API</strong> access.`,
			`Copy the app's ID (under its name) and its <strong>secret</strong>.`,
		},
		ClientID:     `the ID under the app's name`,
		Confidential: `the secret`,
		Notes: []template.HTML{
			`Araldo only reads results (<code>adsread</code>); it never spends.`,
		},
	},
}
