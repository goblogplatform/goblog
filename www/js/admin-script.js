// take the form data and use the patch method to update the settings
function updateSettings(redirect) {
    $("#ajax-error").hide();
    var currentTheme = $("#theme").val();
    var settings = [];

    // iterate over all input fields in the form and create a json object with the key, value, and type
    $("#settings-form :input").each(function() {
        var key = this.name;
        var type = this.tagName === "TEXTAREA" ? "textarea" : (this.tagName === "SELECT" ? "text" : this.type);
        var value = this.value

        if (type === "file") {
            // just get the filename without the path
            if (this.url) {
                value = this.url
            } else {
                if (redirect !== undefined) {
                    // this is the startup wizard, make sure we have some default value or they won't be created
                    // at all
                    if (this.name === "favicon") {
                        value = "/img/favicon.ico"
                    } else if (this.name === "landing_page_image") {
                        value = "/img/profile.png"
                    }
                } else {
                    // otherwise if the value isn't filled in, leave it what it was so it doesn't get erased
                    return
                }
            }
        } else if (type === "submit") {
            return
        } else if (type === "checkbox") {
            // a checkbox's .value is "on" whether or not it is ticked
            value = this.checked ? "true" : "false"
        }
        settings.push(
            {"key": key, "value": value, "type": type}
        )
    });
    console.log("PATCH: " + JSON.stringify(settings));

    $.ajax({
        url: "/api/v1/settings",
        type: "patch",
        dataType: "json",
        contentType: "application/json",
        success: function(json) {
            // change #ajax-error to success and show "settings updated"
            $("#ajax-error").html("Settings updated").show();
            $("#ajax-error").removeClass("alert-danger").addClass("alert-success");

            if (redirect !== undefined) {
                window.location = redirect;
            }

            // Reload if theme was actually changed so new templates take effect
            var newTheme = $("#theme").val();
            if (newTheme !== currentTheme && redirect === undefined) {
                window.location.reload();
            }
        },
        error: function(jqXHR, textStatus, errorThrown) {
            // show #ajax-error with the error message
            $("#ajax-error").html("ERROR: " + textStatus + " " + errorThrown).show();
            $("#ajax-error").removeClass("alert-success").addClass("alert-danger");
        },
        data: JSON.stringify(settings)
    });
}

// this is mostly used by the settings page to upload a file. Post uploads use a different method since they can
// be pasted directly into the editor, however, on the backend they use the same post method to save the file
function uploadFile(fileInput) {
    console.log("uploading file: " + fileInput.files[0].name);
    var formData = new FormData();
    var file = fileInput.files[0];
    formData.append("file", file);
    $.ajax({
        url: "/api/v1/upload",
        type: "post",
        data: formData,
        processData: false,
        contentType: false,
        success: function(json) {
            var url = json.filename;
            console.log("uploaded file: " + url);
            // insert the url into the settings form so that when it gets submitted it will be saved in the db
            fileInput.url = url;
        },
        error: function(jqXHR, textStatus, errorThrown) {
            console.log("ERROR: " + textStatus + " " + errorThrown);
        }
    });
}

function updatePost(id, publish) {
    var tags = $("#tags").text().split(',')
        .map(function(t) { return t.trim(); })
        .filter(function(t) { return t !== ""; })
        .map(function(t) { return {"name": t}; });
    var time = $("#created_at").val();
    var vtime = moment.utc(time)

    var postTypeId = parseInt($("#post_type_id").val()) || 0;

    var post = {
        "id": id,
        "created_at": vtime,
        "title": $("#title").text(),
        "content": simplemde.value(),
        "tags": tags,
        "slug": $("#slug").text(),
        "draft": !publish,
        "post_type_id": postTypeId,
    }
    console.log("PATCH: " + JSON.stringify(post));

    $.ajax({
        url: "/api/v1/posts",
        type: "patch",
        dataType: "json",
        contentType: "application/json",
        success: function(json) {
            var time = moment(json.created_at);
            var slug = json.slug;
            var typeSlug = json.post_type && json.post_type.slug ? json.post_type.slug : "posts";
            window.location.href="/admin/" + typeSlug + "/" + time.format("YYYY/MM/DD") + "/" + slug;
        },
        data: JSON.stringify(post)
    });
}

function createPost(publish) {
    var tags = $("#tags").val().split(',')
        .map(function(t) { return t.trim(); })
        .filter(function(t) { return t !== ""; })
        .map(function(t) { return {"name": t}; });

    var time = $("#created_at").val();
    var vtime = moment.utc(time)

    var postTypeId = parseInt($("#post_type_id").val()) || 0;

    var post = {
        "title": $("#title").val(),
        "created_at": vtime,
        "content": simplemde.value(),
        "tags": tags,
        "draft": !publish,
        "post_type_id": postTypeId,
    }
    console.log("POST: " + JSON.stringify(post));

    $.ajax({
        url: "/api/v1/posts",
        type: "post",
        dataType: "json",
        contentType: "application/json",
        success: function(json) {
            var time = moment(json.created_at);
            var slug = json.slug;
            var typeSlug = json.post_type && json.post_type.slug ? json.post_type.slug : "posts";
            window.location.href="/" + typeSlug + "/" + time.format("YYYY/MM/DD") + "/" + slug;
        },
        error: function(jqXHR, textStatus, errorThrown) {
            alert("ERROR: " + textStatus + " " + errorThrown);
        },
        data: JSON.stringify(post)
    });
}

function publishPost(id) {
    $.ajax({
        url: "/api/v1/publish/" + id,
        type: "patch",
        success: function(json) {
            var time = moment(json.created_at);
            var slug = json.slug;
            var typeSlug = json.post_type && json.post_type.slug ? json.post_type.slug : "posts";
            window.location.href="/" + typeSlug + "/" + time.format("YYYY/MM/DD") + "/" + slug;
        }
    })
}

function draftPost(id) {
    $.ajax({
        url: "/api/v1/draft/" + id,
        type: "patch",
        success: function(json) {
            var time = moment(json.created_at);
            var slug = json.slug;
            var typeSlug = json.post_type && json.post_type.slug ? json.post_type.slug : "posts";
            window.location.href="/" + typeSlug + "/" + time.format("YYYY/MM/DD") + "/" + slug;
        }
    })
}

function deletePost(id) {
    var post = {
        "id": id,
        "title": $("#title").text(),
        "content": $("#content").text(),
    }
    console.log("PATCH: " + JSON.stringify(post));

    $.ajax({
        url: "/api/v1/posts",
        type: "delete",
        dataType: "json",
        contentType: "application/json",
        success: function(json) {
            window.location.href = "/admin/";
        },
        data: JSON.stringify(post)
    });
    return false;
}

function rollbackRevision(postId, revisionId) {
    if (!confirm("Restore this revision? The current version will be saved as a new revision.")) {
        return false;
    }
    $.ajax({
        url: "/api/v1/revisions/" + postId + "/rollback/" + revisionId,
        type: "post",
        contentType: "application/json",
        success: function(json) {
            window.location.reload();
        },
        error: function(jqXHR, textStatus, errorThrown) {
            alert("ERROR: " + textStatus + " " + errorThrown);
        }
    });
    return false;
}

function togglePluginEnabled(checkbox) {
    var pluginName = $(checkbox).data("plugin");
    var enabled = checkbox.checked ? "true" : "false";

    // Save the enabled setting immediately
    var settings = [{"key": pluginName + ".enabled", "value": enabled, "type": "text"}];
    $.ajax({
        url: "/api/v1/plugin-settings",
        type: "patch",
        dataType: "json",
        contentType: "application/json",
        success: function(json) {
            $("#ajax-error").html(pluginName + (checkbox.checked ? " enabled" : " disabled")).show();
            $("#ajax-error").removeClass("alert-danger").addClass("alert-success");
        },
        error: function(jqXHR, textStatus, errorThrown) {
            checkbox.checked = !checkbox.checked; // revert on failure
            $("#ajax-error").html("ERROR: " + textStatus + " " + errorThrown).show();
            $("#ajax-error").removeClass("alert-success").addClass("alert-danger");
        },
        data: JSON.stringify(settings)
    });
}

function updatePluginSettings(btn) {
    $("#ajax-error").hide();
    var settings = [];
    var $card = $(btn).closest(".card");
    var $form = $(btn).closest("form");

    // Include the enabled checkbox from the card header
    var $toggle = $card.find(".plugin-enable-toggle");
    if ($toggle.length) {
        settings.push({"key": $toggle.attr("name"), "value": $toggle.is(":checked") ? "true" : "false", "type": "text"});
    }

    $form.find(":input").each(function() {
        var key = this.name;
        var type = this.tagName === "TEXTAREA" ? "textarea" : "text";
        var value = this.value;
        if (this.type === "submit" || this.type === "button" || !key) return;
        // Password inputs render empty so the stored secret never reaches the
        // page; an empty one means "keep the current value", not "clear it".
        if (this.type === "password" && value === "") return;
        settings.push({"key": key, "value": value, "type": type});
    });

    $.ajax({
        url: "/api/v1/plugin-settings",
        type: "patch",
        dataType: "json",
        contentType: "application/json",
        success: function(json) {
            $("#ajax-error").html("Plugin settings updated").show();
            $("#ajax-error").removeClass("alert-danger").addClass("alert-success");
        },
        error: function(jqXHR, textStatus, errorThrown) {
            $("#ajax-error").html("ERROR: " + textStatus + " " + errorThrown).show();
            $("#ajax-error").removeClass("alert-success").addClass("alert-danger");
        },
        data: JSON.stringify(settings)
    });
}
