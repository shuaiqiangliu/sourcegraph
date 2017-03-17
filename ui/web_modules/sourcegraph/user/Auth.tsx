import { History } from "history";
import * as React from "react";

import { context } from "sourcegraph/app/context";
import { Router, RouterLocation } from "sourcegraph/app/router";
import { LoggableEvent } from "sourcegraph/tracking/constants/AnalyticsConstants";
import { Events } from "sourcegraph/tracking/constants/AnalyticsConstants";
import { oauthProvider, urlToOAuth } from "sourcegraph/util/urlTo";

export interface PartialRouterLocation {
	pathname: string;
	hash: string;
}

export function addQueryObjToURL(
	base: RouterLocation,
	urlOrPathname: string | PartialRouterLocation,
	queryObj: History.Query
): RouterLocation {
	if (typeof urlOrPathname === "string") {
		urlOrPathname = { pathname: urlOrPathname } as RouterLocation;
	}
	return { ...base, ...urlOrPathname, query: { modal: "afterSignup", ...queryObj } };
}

export const defaultOnboardingPath = "/github.com/sourcegraph/checkup/-/blob/fs.go";

/**
 * An action form contains an JSX element that must be included in the DOM and
 * an action to submit that form.
 */
interface ActionForm {
	submit: () => void;
	form: JSX.Element;
}

export interface AuthProps {
	eventObject: LoggableEvent;
	pageName?: string;

	provider: oauthProvider;
	scopes: string;
	returnTo?: string | RouterLocation;
	newUserReturnTo?: string | RouterLocation;
}

/**
 * Get an authorization action and form.
 */
function getAuthAction(props: AuthProps): ActionForm {
	let url = urlToOAuth(
		props.provider,
		props.scopes,
		props.returnTo || null,
		props.newUserReturnTo || null,
	);

	let authForm: HTMLFormElement | null = null;
	const submitAuthForm = () => {
		props.eventObject.logEvent({ page_name: props.pageName || "" });
		if (authForm) {
			authForm.submit();
		}
	};

	return {
		submit: submitAuthForm,
		form: <form
			action={url}
			method="POST"
			ref={el => authForm = el}
			style={{ display: "none" }} >
			<input type="hidden" name="gorilla.csrf.Token" value={context.csrfToken} />
		</form>
	};
}

export function githubAuthAction(router: Router, privateCode: boolean): ActionForm {
	const newUserReturnTo = {
		...router.location,
		pathname: defaultOnboardingPath,
		query: {
			tour: "signup",
			private: privateCode,
			modal: "afterSignup",
		},
		hash: "#L153"
	};
	return getAuthAction({
		eventObject: Events.OAuth2FlowGitHub_Initiated,
		provider: "github",
		scopes: privateCode ? "read:org,user:email,repo" : "read:org,user:email",
		returnTo: router.location,
		newUserReturnTo,
	});
}
