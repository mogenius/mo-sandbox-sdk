using System.Text.Json;

namespace Mogenius.Sandboxes;

/// <summary>Who decided an error: the platform API, the cluster's operator, or the SDK itself.</summary>
public enum ErrorSource
{
    /// <summary>The platform API.</summary>
    Api,
    /// <summary>The mogenius operator in the cluster.</summary>
    Operator,
    /// <summary>The SDK, before or after talking to the platform.</summary>
    Sdk,
}

/// <summary>
/// Base of every error the SDK throws. Carries what the platform said so a caller can branch on
/// <see cref="ErrorCode"/> or <see cref="StatusCode"/> without parsing text. One class per failure kind.
/// </summary>
public class MogeniusException : Exception
{
    /// <summary>HTTP status of the platform's answer, when there was one.</summary>
    public int? StatusCode { get; }

    /// <summary>The platform's error code, e.g. <c>SANDBOX_NOT_FOUND</c>.</summary>
    public string? ErrorCode { get; }

    /// <summary>Who decided: the platform API or the cluster's operator.</summary>
    public ErrorSource ErrorSource { get; }

    /// <summary>Creates an error with what the platform said.</summary>
    public MogeniusException(string message, int? statusCode = null, string? errorCode = null,
        ErrorSource source = ErrorSource.Sdk, Exception? innerException = null)
        : base(message, innerException)
    {
        StatusCode = statusCode;
        ErrorCode = errorCode;
        ErrorSource = source;
    }
}

/// <summary>The key was rejected (401).</summary>
public class MogeniusUnauthorizedException(string message, int? statusCode = null, string? errorCode = null,
    ErrorSource source = ErrorSource.Sdk) : MogeniusException(message, statusCode, errorCode, source);

/// <summary>The key may not do this (403): no EDITOR grant, no cluster role, or RBAC in the cluster.</summary>
public class MogeniusForbiddenException(string message, int? statusCode = null, string? errorCode = null,
    ErrorSource source = ErrorSource.Sdk) : MogeniusException(message, statusCode, errorCode, source);

/// <summary>No sandbox, container, profile, session or command of that name (404).</summary>
public class MogeniusNotFoundException(string message, int? statusCode = null, string? errorCode = null,
    ErrorSource source = ErrorSource.Sdk) : MogeniusException(message, statusCode, errorCode, source);

/// <summary>
/// The sandbox or session is in the wrong state for this: not bound yet, suspended, name taken, a command still
/// running (409).
/// </summary>
public class MogeniusConflictException(string message, int? statusCode = null, string? errorCode = null,
    ErrorSource source = ErrorSource.Sdk) : MogeniusException(message, statusCode, errorCode, source);

/// <summary>The request itself is wrong (400): a bad label, env name, or parameter.</summary>
public class MogeniusValidationException(string message, int? statusCode = null, string? errorCode = null,
    ErrorSource source = ErrorSource.Sdk) : MogeniusException(message, statusCode, errorCode, source);

/// <summary>A platform-side wait ran out (504 from the operator, or the SDK's own deadline).</summary>
public class MogeniusTimeoutException(string message, int? statusCode = null, string? errorCode = null,
    ErrorSource source = ErrorSource.Sdk) : MogeniusException(message, statusCode, errorCode, source);

/// <summary>The command ran longer than its timeout and was stopped (408).</summary>
public class MogeniusProcessExecutionTimeoutException(string message, int? statusCode = null,
    string? errorCode = null, ErrorSource source = ErrorSource.Sdk)
    : MogeniusTimeoutException(message, statusCode, errorCode, source);

/// <summary>The cluster's operator is too old for this call (424).</summary>
public class MogeniusOperatorUpgradeRequiredException(string message, int? statusCode = null,
    string? errorCode = null, ErrorSource source = ErrorSource.Sdk)
    : MogeniusException(message, statusCode, errorCode, source);

/// <summary>Not available for Kubernetes sandboxes: fork, pause, archive, snapshots of running state, computer use.</summary>
public class MogeniusUnsupportedException(string feature, string? alternative = null) : MogeniusException(
    $"{feature} is not supported by mogenius sandboxes (they are Kubernetes pods, not micro-VMs)." +
    (alternative is null ? "" : $" {alternative}"),
    errorCode: "UNSUPPORTED", source: ErrorSource.Sdk);

/// <summary>Error codes the SDK itself sets.</summary>
public static class MogeniusErrorCodes
{
    /// <summary>
    /// An upload over SFTP that found no way into the sandbox: the stream gateway or the cluster's SSH gateway
    /// refused the connection, cannot do mode=ssh or is switched off. Nothing was written; the caller decides
    /// whether to upload over HTTP instead.
    /// </summary>
    public const string SftpUnavailable = "SFTP_UNAVAILABLE";
}

/// <summary>Builds the right error class from an HTTP failure of the platform API.</summary>
public static class MogeniusErrors
{
    /// <summary>
    /// The error an HTTP failure of the platform stands for: by <c>errorCode</c> first, by status second.
    /// </summary>
    /// <param name="status">HTTP status</param>
    /// <param name="body">the response body: the platform's JSON error body, plain text, or null</param>
    /// <param name="fallback">message when the body has none</param>
    public static MogeniusException FromResponse(int status, string? body, string fallback)
    {
        var (message, errorCode, source) = ParseBody(body, fallback);
        return Create(status, message, errorCode, source);
    }

    internal static MogeniusException Create(int? status, string message, string? errorCode, ErrorSource source)
    {
        switch (errorCode)
        {
            case "EXEC_TIMEOUT":
                return new MogeniusProcessExecutionTimeoutException(message, status, errorCode, source);
            case "OPERATOR_TIMEOUT":
                return new MogeniusTimeoutException(message, status, errorCode, source);
            case "OPERATOR_UPGRADE_REQUIRED":
                return new MogeniusOperatorUpgradeRequiredException(message, status, errorCode, source);
            case "SANDBOX_NOT_FOUND":
            case "CONTAINER_NOT_FOUND":
            case "SANDBOX_PROFILE_NOT_FOUND":
            case "FILE_NOT_FOUND":
            case "SESSION_NOT_FOUND":
            case "SESSION_COMMAND_NOT_FOUND":
                return new MogeniusNotFoundException(message, status, errorCode, source);
            case "SANDBOX_NOT_READY":
            case "SANDBOX_ALREADY_EXISTS":
            case "FILE_EXISTS":
            case "FILE_NOT_EMPTY":
            case "SESSION_ALREADY_EXISTS":
            case "SESSION_BUSY":
                return new MogeniusConflictException(message, status, errorCode, source);
            case "FILE_PERMISSION_DENIED":
                return new MogeniusForbiddenException(message, status, errorCode, source);
            case "INVALID_REQUEST":
            case "SANDBOX_ENV_NOT_ALLOWED":
                return new MogeniusValidationException(message, status, errorCode, source);
        }

        return status switch
        {
            400 => new MogeniusValidationException(message, status, errorCode, source),
            401 => new MogeniusUnauthorizedException(message, status, errorCode, source),
            403 => new MogeniusForbiddenException(message, status, errorCode, source),
            404 => new MogeniusNotFoundException(message, status, errorCode, source),
            408 => new MogeniusProcessExecutionTimeoutException(message, status, errorCode, source),
            409 => new MogeniusConflictException(message, status, errorCode, source),
            424 => new MogeniusOperatorUpgradeRequiredException(message, status, errorCode, source),
            504 => new MogeniusTimeoutException(message, status, errorCode, source),
            _ => new MogeniusException(message, status, errorCode, source),
        };
    }

    /// <summary>Recreates <paramref name="error"/> as the same class with a new message and error code.</summary>
    internal static MogeniusException Rewrap(MogeniusException error, string message, string errorCode)
    {
        var (status, source) = (error.StatusCode, error.ErrorSource);
        return error switch
        {
            MogeniusProcessExecutionTimeoutException =>
                new MogeniusProcessExecutionTimeoutException(message, status, errorCode, source),
            MogeniusTimeoutException => new MogeniusTimeoutException(message, status, errorCode, source),
            MogeniusUnauthorizedException => new MogeniusUnauthorizedException(message, status, errorCode, source),
            MogeniusForbiddenException => new MogeniusForbiddenException(message, status, errorCode, source),
            MogeniusNotFoundException => new MogeniusNotFoundException(message, status, errorCode, source),
            MogeniusConflictException => new MogeniusConflictException(message, status, errorCode, source),
            MogeniusValidationException => new MogeniusValidationException(message, status, errorCode, source),
            MogeniusOperatorUpgradeRequiredException =>
                new MogeniusOperatorUpgradeRequiredException(message, status, errorCode, source),
            _ => new MogeniusException(message, status, errorCode, source),
        };
    }

    private static (string Message, string? ErrorCode, ErrorSource Source) ParseBody(string? body, string fallback)
    {
        if (string.IsNullOrEmpty(body))
        {
            return (fallback, null, ErrorSource.Sdk);
        }

        try
        {
            using var document = JsonDocument.Parse(body);
            var root = document.RootElement;
            if (root.ValueKind != JsonValueKind.Object)
            {
                return (root.ValueKind == JsonValueKind.String ? root.GetString() ?? fallback : body, null,
                    ErrorSource.Sdk);
            }

            string? message = null;
            if (root.TryGetProperty("message", out var messageElement))
            {
                message = messageElement.ValueKind switch
                {
                    JsonValueKind.Array => string.Join(", ",
                        messageElement.EnumerateArray().Select(item => item.ValueKind == JsonValueKind.String
                            ? item.GetString()
                            : item.GetRawText())),
                    JsonValueKind.String => messageElement.GetString(),
                    _ => null,
                };
            }

            if (string.IsNullOrEmpty(message) && root.TryGetProperty("error", out var errorElement) &&
                errorElement.ValueKind == JsonValueKind.String)
            {
                message = errorElement.GetString();
            }

            var errorCode = root.TryGetProperty("errorCode", out var codeElement) &&
                            codeElement.ValueKind == JsonValueKind.String
                ? codeElement.GetString()
                : null;
            var source = root.TryGetProperty("source", out var sourceElement) &&
                         sourceElement.ValueKind == JsonValueKind.String
                ? sourceElement.GetString() switch
                {
                    "operator" => ErrorSource.Operator,
                    "api" => ErrorSource.Api,
                    _ => ErrorSource.Sdk,
                }
                : ErrorSource.Sdk;
            return (string.IsNullOrEmpty(message) ? fallback : message, errorCode, source);
        }
        catch (JsonException)
        {
            // not JSON: the text is the message
            return (body, null, ErrorSource.Sdk);
        }
    }
}
